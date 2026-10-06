package main

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"
	"go.temporal.io/server/temporal"
)

// serviceEnvVar is the environment variable the upstream Temporal server
// binary and the Helm chart use to select the roles of a process.
const serviceEnvVar = "TEMPORAL_SERVICES"

var (
	// ErrUnknownService is returned when a requested role is not a Temporal service.
	ErrUnknownService = errors.New("unknown temporal service")
	// ErrServiceNotInConfig is returned when a requested role has no entry in the
	// loaded config, and therefore no ports to listen on.
	ErrServiceNotInConfig = errors.New("temporal service is not declared in the config")
)

// resolveServices decides which Temporal roles this process starts.
//
// Resolution order:
//  1. service holds the values of --service / TEMPORAL_SERVICES. It must be
//     nil when the flag was not set, so the flag default never wins.
//  2. Otherwise legacy, the deprecated --services value, is split on ",".
//     usedLegacy reports that this branch was taken, so the caller can warn.
//  3. Otherwise every service declared in the loaded config (declared) runs,
//     which is the behaviour for single-process and local setups.
//
// Entries are trimmed, blank entries are dropped, duplicates are removed and
// the result is sorted. Every name must be a known Temporal service
// (temporal.Services) and must be declared in the config, because the config
// carries the ports of the service.
func resolveServices(service []string, legacy string, declared []string) (services []string, usedLegacy bool, err error) {
	requested := cleanServiceNames(service)

	if len(requested) == 0 {
		requested = cleanServiceNames(strings.Split(legacy, ","))
		usedLegacy = len(requested) > 0
	}

	if len(requested) == 0 {
		requested = cleanServiceNames(declared)
	}

	for _, name := range requested {
		if !slices.Contains(temporal.Services, name) {
			return nil, usedLegacy, fmt.Errorf("%w: %q (valid: %s)", ErrUnknownService, name, strings.Join(temporal.Services, ", "))
		}

		if !slices.Contains(declared, name) {
			return nil, usedLegacy, fmt.Errorf("%w: %q (declared: %s)", ErrServiceNotInConfig, name, strings.Join(sortedCopy(declared), ", "))
		}
	}

	return requested, usedLegacy, nil
}

// cleanServiceNames trims entries, drops blanks and duplicates and sorts.
func cleanServiceNames(names []string) []string {
	out := make([]string, 0, len(names))

	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(out, name) {
			continue
		}

		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

func sortedCopy(names []string) []string {
	out := slices.Clone(names)
	sort.Strings(out)

	return out
}

// serviceFlagValues returns the --service values and the deprecated
// --services value of the start command. Each is empty unless explicitly set
// by flag or environment, so flag defaults can never mask the config fallback.
func serviceFlagValues(cmd *cli.Command) (service []string, legacy string) {
	if cmd.IsSet("service") {
		service = cmd.StringSlice("service")
	}

	if cmd.IsSet("services") {
		legacy = cmd.String("services")
	}

	return service, legacy
}
