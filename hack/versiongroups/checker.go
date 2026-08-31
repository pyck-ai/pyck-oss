package main

import (
	"fmt"
	"os"
	"sort"
)

// violation is one member occurrence whose (normalized) value disagrees
// with its group's source of truth.
type violation struct {
	group      string
	memberDesc string // human label for the member Ref, e.g. "member[1]"
	file       string
	line       int
	expected   string // source's normalized value, for display
	found      string // this occurrence's normalized value, for display
	fixSpan    occurrence
	fixValue   string // the raw replacement text FixValue produced
}

// checkGroup evaluates one manifest group against the repo at repoRoot. It
// returns the violations found and appends any configuration errors
// (a Ref that matched zero files/lines and isn't Optional, or a source
// value a member's normalizer can't parse) to configErrs.
func checkGroup(repoRoot, name string, g *Group, configErrs *[]string) []violation {
	srcFiles, err := resolveFiles(repoRoot, g.Source)
	if err != nil {
		*configErrs = append(*configErrs, fmt.Sprintf("group %q source: %v", name, err))
		return nil
	}
	srcOccs, err := findOccurrences(repoRoot, g.Source, srcFiles)
	if err != nil {
		*configErrs = append(*configErrs, fmt.Sprintf("group %q source: %v", name, err))
		return nil
	}
	if len(srcOccs) == 0 {
		*configErrs = append(*configErrs, fmt.Sprintf(
			"group %q source: pattern %q matched nothing in %v", name, g.Source.Pattern, srcFiles))
		return nil
	}
	// If the source pattern matches more than one line, the first match
	// (in file, then position, order) is authoritative — the same
	// `head -1` behavior the old scripts used for go.work/go.mod.
	sourceRaw := srcOccs[0].raw

	var violations []violation
	for i, member := range g.Members {
		memberDesc := fmt.Sprintf("member[%d] (%s)", i, memberLabel(member))
		normalizer := normalizers[member.Normalize]

		expected, ok := normalizer.Normalize(sourceRaw)
		if !ok {
			*configErrs = append(*configErrs, fmt.Sprintf(
				"group %q %s: source value %q does not parse under normalize:%q",
				name, memberDesc, sourceRaw, member.Normalize))
			continue
		}

		files, err := resolveFiles(repoRoot, member)
		if err != nil {
			*configErrs = append(*configErrs, fmt.Sprintf("group %q %s: %v", name, memberDesc, err))
			continue
		}
		occs, err := findOccurrences(repoRoot, member, files)
		if err != nil {
			*configErrs = append(*configErrs, fmt.Sprintf("group %q %s: %v", name, memberDesc, err))
			continue
		}
		if len(occs) == 0 && !member.Optional {
			*configErrs = append(*configErrs, fmt.Sprintf(
				"group %q %s: pattern %q matched nothing in %v (mark `optional: true` if that's expected)",
				name, memberDesc, member.Pattern, files))
			continue
		}

		for _, occ := range occs {
			found, ok := normalizer.Normalize(occ.raw)
			if !ok {
				// Not a value this normalizer understands (e.g. a
				// floating ":latest" tag under leading-semver) — not a
				// pinned reference, so there's nothing to compare.
				continue
			}
			if found == expected {
				continue
			}
			violations = append(violations, violation{
				group:      name,
				memberDesc: memberDesc,
				file:       occ.file,
				line:       occ.line,
				expected:   expected,
				found:      found,
				fixSpan:    occ,
				fixValue:   normalizer.FixValue(occ.raw, expected),
			})
		}
	}
	return violations
}

func memberLabel(r Ref) string {
	if r.File != "" {
		return r.File
	}
	return r.Glob
}

// applyFixes rewrites every violation's file in place, replacing each
// captured span with its computed fixValue. Fixes are applied per file,
// from the highest byte offset to the lowest, so earlier spans in the same
// file stay valid as later ones are rewritten.
func applyFixes(repoRoot string, violations []violation) error {
	byFile := map[string][]violation{}
	for _, v := range violations {
		byFile[v.fixSpan.file] = append(byFile[v.fixSpan.file], v)
	}

	for file, vs := range byFile {
		sort.Slice(vs, func(i, j int) bool { return vs[i].fixSpan.spanStart > vs[j].fixSpan.spanStart })

		path := repoRoot + "/" + file
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
		for _, v := range vs {
			data = append(data[:v.fixSpan.spanStart], append([]byte(v.fixValue), data[v.fixSpan.spanEnd:]...)...)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", file, err)
		}
		if err := os.WriteFile(path, data, info.Mode()); err != nil {
			return fmt.Errorf("writing %s: %w", file, err)
		}
	}
	return nil
}
