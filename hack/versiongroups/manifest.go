package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/goccy/go-yaml"
)

// Ref describes one location a version string is read from: either the
// authoritative "source" of a group, or one of its "members" that must
// match the source.
type Ref struct {
	// File is an explicit path relative to the repo root. Mutually
	// exclusive with Glob.
	File string `yaml:"file,omitempty"`
	// Glob is a pathspec passed straight to `git ls-files` (the same
	// double-star patterns the old bash scripts used, e.g. "**/go.mod").
	// Mutually exclusive with File.
	Glob string `yaml:"glob,omitempty"`
	// Pattern is a Go RE2 regular expression with exactly one capture
	// group, matched line by line against the file content. Every
	// matching line contributes one occurrence.
	Pattern string `yaml:"pattern"`
	// Normalize names how the captured value is reduced before it is
	// compared against the source (see normalize.go). Empty means
	// "compare the captured string exactly as-is".
	Normalize string `yaml:"normalize,omitempty"`
	// Optional means it is fine for this member to match zero lines
	// across every file it resolves to (e.g. the `toolchain` directive,
	// which most go.mod files omit). Without Optional, a member that
	// matches nothing anywhere is a manifest configuration error, not a
	// silently-passing check.
	Optional bool `yaml:"optional,omitempty"`

	compiled *regexp.Regexp
}

// Group is one set of version references that must all agree, anchored to
// a single Source of truth.
type Group struct {
	Source  Ref   `yaml:"source"`
	Members []Ref `yaml:"members"`
}

// Manifest is the top-level shape of .github/renovate-versiongroups.yaml.
type Manifest struct {
	Groups map[string]*Group `yaml:"groups"`
}

// loadManifest reads and parses the manifest at path, compiles every
// pattern up front (so a typo in a regex is reported once, clearly,
// instead of surfacing lazily mid-check), and validates the File/Glob
// exclusivity and normalizer name for every Ref.
func loadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}

	if len(m.Groups) == 0 {
		return nil, fmt.Errorf("manifest %s defines no groups", path)
	}

	for name, g := range m.Groups {
		if err := compileRef(&g.Source); err != nil {
			return nil, fmt.Errorf("group %q source: %w", name, err)
		}
		if g.Source.Optional {
			return nil, fmt.Errorf("group %q source: a group's source cannot be optional", name)
		}
		if len(g.Members) == 0 {
			return nil, fmt.Errorf("group %q defines no members", name)
		}
		for i := range g.Members {
			if err := compileRef(&g.Members[i]); err != nil {
				return nil, fmt.Errorf("group %q member %d: %w", name, i, err)
			}
		}
	}

	return &m, nil
}

// compileRef validates a Ref's shape and pre-compiles its pattern.
func compileRef(r *Ref) error {
	if (r.File == "") == (r.Glob == "") {
		return fmt.Errorf("exactly one of file/glob must be set (file=%q glob=%q)", r.File, r.Glob)
	}
	if r.Pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	if _, ok := normalizers[r.Normalize]; !ok {
		return fmt.Errorf("unknown normalize %q (valid: \"\", %q, %q)", r.Normalize, normalizeLeadingSemver, normalizeMajorMinor)
	}

	re, err := regexp.Compile(`(?m)` + r.Pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", r.Pattern, err)
	}
	if re.NumSubexp() != 1 {
		return fmt.Errorf("pattern %q must have exactly one capture group, has %d", r.Pattern, re.NumSubexp())
	}
	r.compiled = re
	return nil
}

// sortedGroupNames returns the manifest's group names in a stable
// (alphabetical) order so output and --fix behavior are deterministic
// across runs, independent of Go's randomized map iteration.
func (m *Manifest) sortedGroupNames() []string {
	names := make([]string, 0, len(m.Groups))
	for name := range m.Groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
