// Command versiongroups checks that groups of version references declared
// in .github/renovate-versiongroups.yaml agree with each other, and can optionally
// rewrite the disagreeing ones to match. It replaces the old
// scripts/check-go-versions.sh and scripts/check-temporal-versions.sh with
// one declarative, generic tool: adding a new version group (or a new file
// pattern to an existing one) is a manifest edit, not a new bash script.
//
// # --fix must NEVER run in automation
//
// --fix is for local development only. Running it in CI (or any other
// unattended context) is actively dangerous: on a Renovate branch where a
// Go module (or the Temporal server, or any other tracked dependency) has
// bumped but its paired Docker image has not published yet, --fix would
// happily rewrite the Docker image tag UP to a version that does not exist
// on the registry. That turns a correctly-caught desync into a GREEN lint
// run followed by a runtime image-pull failure — strictly worse than
// leaving the check red. CI must only ever invoke this tool in its default
// (check-only) mode.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const defaultManifestPath = ".github/renovate-versiongroups.yaml"

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: versiongroups [OPTIONS]

Check version consistency across the repo using the group definitions in
%s. Each group names one source-of-truth file/pattern and a list of
member file/pattern references that must match it (after an optional
normalization, e.g. truncating to major.minor).

OPTIONS:
  -manifest PATH  Path to the manifest, relative to the repo root
                   (default: %s)
  --silent         Suppress non-error output
  --dry-run        Show what --fix would change, without writing anything
  --fix            Rewrite mismatched references to match their source
  --help           Show this help message

  ############################################################
  # --fix must NEVER be used in CI / automation. See the      #
  # package doc comment at the top of main.go for why: on a   #
  # Renovate branch it can rewrite a Docker tag up to a       #
  # version that hasn't published yet, turning a caught       #
  # desync into a green build with a broken image pull.       #
  ############################################################

EXIT CODES:
  0 - all version groups are consistent
  1 - version inconsistencies found
  2 - script/configuration error (bad manifest, unmatched pattern, etc.)

  NOTE: 1 and 2 are only distinguishable when running a BUILT BINARY.
  `+"`go run`"+` collapses every non-zero program exit to 1 (it reports the
  real code as "exit status N" on stderr, but exits 1 itself). Both CI and
  the Taskfile invoke this via `+"`go run`"+`, so callers there see 0 or 1
  only. That is fine for the CI gate — any non-zero fails the branch and
  withholds the Renovate PR — and the printed message still says plainly
  whether it was a mismatch or a manifest problem. Build the binary if you
  ever need to branch on 2 programmatically.
`, defaultManifestPath, defaultManifestPath)
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		manifestPath = flag.String("manifest", defaultManifestPath, "path to renovate-versiongroups.yaml, relative to repo root")
		silent       = flag.Bool("silent", false, "suppress non-error output")
		dryRun       = flag.Bool("dry-run", false, "show what --fix would change, without writing")
		fixMode      = flag.Bool("fix", false, "rewrite mismatched references to match their source")
		help         = flag.Bool("help", false, "show usage")
	)
	flag.Usage = usage
	flag.Parse()

	if *help {
		usage()
		return 0
	}
	if *dryRun {
		*fixMode = true // matches the old scripts: --dry-run implies fix analysis, minus the writes
	}

	log := func(format string, args ...any) {
		if !*silent {
			fmt.Println(fmt.Sprintf(format, args...))
		}
	}
	errorf := func(format string, args ...any) {
		fmt.Fprintln(os.Stderr, "❌ "+fmt.Sprintf(format, args...))
	}

	repoRoot, err := gitRoot()
	if err != nil {
		errorf("not in a git repository: %v", err)
		return 2
	}

	manifest, err := loadManifest(repoRoot + "/" + *manifestPath)
	if err != nil {
		errorf("%v", err)
		return 2
	}

	log("Checking version group consistency across project files...")
	log("Project root: %s", repoRoot)
	log("Using manifest: %s", *manifestPath)

	violations, configErrs := checkAll(repoRoot, manifest, log)
	if len(configErrs) > 0 {
		for _, e := range configErrs {
			errorf("%s", e)
		}
		return 2
	}

	if len(violations) == 0 {
		log("✅ All version groups are consistent!")
		return 0
	}

	for _, v := range violations {
		errorf("[%s] %s in %s:%d: expected %s, found %s", v.group, v.memberDesc, v.file, v.line, v.expected, v.found)
	}

	if !*fixMode {
		errorf("Version consistency check failed!")
		log("💡 Run with --fix locally to automatically fix the issues (never in CI — see --help)")
		return 1
	}

	if *dryRun {
		log("🔍 Dry run - showing what would be fixed:")
		for _, v := range violations {
			log("- [DRY RUN] %s:%d: %s → %s", v.file, v.line, v.found, v.fixValue)
		}
		return 0
	}

	log("🔧 Applying fixes...")
	for _, v := range violations {
		log("- Updating %s:%d: %s → %s", v.file, v.line, v.found, v.fixValue)
	}
	if err := applyFixes(repoRoot, violations); err != nil {
		errorf("%v", err)
		return 1
	}

	log("Re-checking after fixes...")
	violations, configErrs = checkAll(repoRoot, manifest, log)
	if len(configErrs) > 0 {
		for _, e := range configErrs {
			errorf("%s", e)
		}
		return 2
	}
	if len(violations) > 0 {
		for _, v := range violations {
			errorf("[%s] %s in %s:%d: expected %s, found %s", v.group, v.memberDesc, v.file, v.line, v.expected, v.found)
		}
		errorf("Some issues remain after applying fixes")
		return 1
	}

	log("✅ Applied fixes; all version groups are now consistent!")
	return 0
}

// checkAll runs checkGroup over every group in the manifest, in
// deterministic (alphabetical) group-name order.
func checkAll(repoRoot string, manifest *Manifest, log func(string, ...any)) ([]violation, []string) {
	var violations []violation
	var configErrs []string
	for _, name := range manifest.sortedGroupNames() {
		log("Checking group %q...", name)
		violations = append(violations, checkGroup(repoRoot, name, manifest.Groups[name], &configErrs)...)
	}
	return violations, configErrs
}

func gitRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
