package main

import (
	"flag"
)

// isBootstrapMode checks if the application was started with the -bootstrap flag.
//
// Deprecated: Use the PYCK_BOOTSTRAP_ENABLED environment variable instead.
func isBootstrapMode() bool {
	bootstrapFlag := flag.Bool("bootstrap", false, "Run in bootstrap mode (deprecated: use PYCK_BOOTSTRAP_ENABLED env var)")
	flag.Parse()
	return *bootstrapFlag
}
