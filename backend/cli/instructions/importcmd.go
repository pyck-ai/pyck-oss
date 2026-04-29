package instructions

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pyck-ai/pyck/backend/common/importexport"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	receivingapi "github.com/pyck-ai/pyck/backend/receiving/api"
)

const (
	dryRunFlagName          = "dry-run"
	continueOnErrorFlagName = "continue-on-error"
)

var errImportPartialFailure = errors.New("import partial failure")

//nolint:gochecknoinits // init required for cobra command setup
func init() {
	importCmd.Flags().Bool(dryRunFlagName, false, "Parse and validate without making changes")
	importCmd.Flags().Bool(continueOnErrorFlagName, false, "Continue importing after errors")

	rootCmd.AddCommand(importCmd)
}

var importCmd = &cobra.Command{
	Use:   "import <file-or-dir>...",
	Short: "Import entities from JSONL or JSON files.",
	Long: `Import entities from JSONL (.jsonl) or JSON (.json) files via the GraphQL API.

Files are processed in command-line order. Directories are expanded to their
contained .jsonl and .json files in alphabetical order. Entities within a file
are processed in definition order.

Entities are identified by __typename and looked up by their identity field
(e.g., name, slug, sku). If an entity exists, it is updated; otherwise, it
is created.

Use $ref syntax to reference other entities by identity field instead of UUID:
  {"locationID": {"$ref": {"__typename": "Location", "name": "Building-A"}}}

Dependencies must be defined before the entities that reference them.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		dryRun, _ := cmd.Flags().GetBool(dryRunFlagName)
		continueOnError, _ := cmd.Flags().GetBool(continueOnErrorFlagName)

		reg, err := buildRegistry(cmd)
		if err != nil {
			return err
		}

		imp := importexport.NewImporter(reg,
			importexport.WithDryRun(dryRun),
			importexport.WithContinueOnError(continueOnError),
		)

		result, err := imp.ImportFiles(cmd.Context(), args)
		if err != nil {
			return err
		}

		if len(result.Errors) > 0 {
			return fmt.Errorf("%w: %d error(s)", errImportPartialFailure, len(result.Errors))
		}
		return nil
	},
}

// buildRegistry creates a registry with all service entities registered.
func buildRegistry(cmd *cobra.Command) (*importexport.Registry, error) {
	reg := importexport.NewRegistry()

	mgmtClient, err := getManagementClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("management client: %w", err)
	}
	if err := managementapi.RegisterEntities(reg, mgmtClient); err != nil {
		return nil, fmt.Errorf("register management entities: %w", err)
	}

	invClient, err := getInventoryClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("inventory client: %w", err)
	}
	if err := inventoryapi.RegisterEntities(reg, invClient); err != nil {
		return nil, fmt.Errorf("register inventory entities: %w", err)
	}

	mdClient, err := getMainDataClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("main-data client: %w", err)
	}
	if err := maindataapi.RegisterEntities(reg, mdClient); err != nil {
		return nil, fmt.Errorf("register main-data entities: %w", err)
	}

	pickClient, err := getPickingClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("picking client: %w", err)
	}
	if err := pickingapi.RegisterEntities(reg, pickClient); err != nil {
		return nil, fmt.Errorf("register picking entities: %w", err)
	}

	recvClient, err := getReceivingClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("receiving client: %w", err)
	}
	if err := receivingapi.RegisterEntities(reg, recvClient); err != nil {
		return nil, fmt.Errorf("register receiving entities: %w", err)
	}

	return reg, nil
}
