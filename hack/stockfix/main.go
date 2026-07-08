package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const helpText = `stockfix — inventory stock ledger corruption analyzer and repair tool

Usage:
  stockfix analyze [flags]            read-only analysis (always safe; exit 3 = violations found)
  stockfix fix     [flags]            repair rollup violations (dry-run by default)
  stockfix workers stop|start|status  manage inventory worker processes

Global flags (available on all subcommands):
  --db-url  string  PostgreSQL connection URL
                    Precedence: flag > PYCK_DATABASE_URL > PYCK_DATABASE_MASTER_URL
  --tenant  uuid    Tenant ID (required for fix; omit for analyze = all tenants)
  --schema  string  PostgreSQL schema name (default: inventory)

Run 'stockfix <subcommand> --help' for subcommand-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, helpText)
		os.Exit(1)
	}

	sub, rest := os.Args[1], os.Args[2:]
	switch sub {
	case "analyze":
		os.Exit(cmdAnalyze(rest))
	case "fix":
		os.Exit(cmdFix(rest))
	case "workers":
		os.Exit(cmdWorkers(rest))
	case "help", "--help", "-h":
		fmt.Print(helpText)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", sub, helpText)
		os.Exit(1)
	}
}

// globalFlags are shared by all subcommands.
type globalFlags struct {
	dbURL    string
	tenantID string
	schema   string
}

func addGlobal(fs *flag.FlagSet, g *globalFlags) {
	fs.StringVar(&g.dbURL, "db-url", "", "PostgreSQL connection URL")
	fs.StringVar(&g.tenantID, "tenant", "", "Tenant UUID")
	fs.StringVar(&g.schema, "schema", "inventory", "Schema name")
}

// resolveDBURL returns flag > PYCK_DATABASE_URL > PYCK_DATABASE_MASTER_URL.
func resolveDBURL(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("PYCK_DATABASE_URL"); v != "" {
		return v
	}
	return os.Getenv("PYCK_DATABASE_MASTER_URL")
}

// parseDuration parses Go duration strings plus "Nd" shorthand (e.g. "7d" = 168h).
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
