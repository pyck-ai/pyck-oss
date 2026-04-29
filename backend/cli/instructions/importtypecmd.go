package instructions

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pyck-ai/pyck/backend/common/importexport"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	receivingapi "github.com/pyck-ai/pyck/backend/receiving/api"
)

var errUnknownImportType = errors.New("unknown import type")

//nolint:gochecknoinits // init required for cobra command setup
func init() {
	rootCmd.AddCommand(importTypeCmd)
}

var importTypeCmd = &cobra.Command{
	Use:   "import-type [typename]",
	Short: "Show information about importable entity types.",
	Long: `Show information about importable entity types.

Without arguments, lists all available types with their identity fields.
With a typename argument, shows details for that specific type.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reg := buildMetadataRegistry()

		if len(args) == 0 {
			return listTypes(reg)
		}
		return showType(reg, args[0])
	},
}

// buildMetadataRegistry creates a registry without API clients. Only the
// metadata (TypeName, Service, IdentityField) is needed.
func buildMetadataRegistry() *importexport.Registry {
	reg := importexport.NewRegistry()

	// Register with nil clients — only metadata (TypeName, IdentityField) is used.
	// Errors here would indicate a code bug (duplicate registration), not a runtime issue.
	_ = managementapi.RegisterEntities(reg, nil) // metadata-only, nil client is safe
	_ = inventoryapi.RegisterEntities(reg, nil)  // metadata-only, nil client is safe
	_ = maindataapi.RegisterEntities(reg, nil)   // metadata-only, nil client is safe
	_ = pickingapi.RegisterEntities(reg, nil)    // metadata-only, nil client is safe
	_ = receivingapi.RegisterEntities(reg, nil)  // metadata-only, nil client is safe

	return reg
}

func listTypes(reg *importexport.Registry) error {
	fmt.Fprintln(os.Stdout, "Available import types:")
	fmt.Fprintln(os.Stdout)
	for _, desc := range reg.All() {
		fmt.Fprintf(os.Stdout, "  %-20s service=%-12s identity=%s\n", desc.TypeName, desc.Service, desc.IdentityField)
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Usage: pyck import-type <typename>")
	return nil
}

func showType(reg *importexport.Registry, typeName string) error {
	desc, ok := reg.Get(typeName)
	if !ok {
		// Try case-insensitive match.
		for _, d := range reg.All() {
			if strings.EqualFold(d.TypeName, typeName) {
				desc = d
				ok = true
				break
			}
		}
	}
	if !ok {
		return fmt.Errorf("%w %q (available: %s)", errUnknownImportType, typeName, strings.Join(reg.TypeNames(), ", "))
	}

	w := os.Stdout
	fmt.Fprintf(w, "Type:     %s\n", desc.TypeName)
	fmt.Fprintf(w, "Service:  %s\n", desc.Service)
	fmt.Fprintf(w, "Identity: %s (used for existence checks during import)\n", desc.IdentityField)
	return nil
}
