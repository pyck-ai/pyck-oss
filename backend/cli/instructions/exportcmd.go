package instructions

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

const (
	exportTypesFlagName = "types"
)

//nolint:gochecknoinits // init required for cobra command setup
func init() {
	exportCmd.Flags().String(exportTypesFlagName, "", "Comma-separated entity types to export (default: all)")

	rootCmd.AddCommand(exportCmd)
}

var exportCmd = &cobra.Command{
	Use:   "export <dir>",
	Short: "Export entities to JSONL files in a directory.",
	Long: `Export entities from the GraphQL API to JSONL format.

Writes one .jsonl file per entity type to the specified directory
(e.g., location.jsonl, repository.jsonl).

Use --types to filter specific entity types.

Server-managed fields (id, tenantID, createdAt, updatedAt, etc.) are stripped.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		typesStr, _ := cmd.Flags().GetString(exportTypesFlagName)

		reg, err := buildRegistry(cmd)
		if err != nil {
			return err
		}

		var typeNames []string
		if typesStr != "" {
			typeNames = strings.Split(typesStr, ",")
			for i := range typeNames {
				typeNames[i] = strings.TrimSpace(typeNames[i])
			}
		}

		exp := importexport.NewExporter(reg)

		dir := args[0]
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return exp.ExportToDir(cmd.Context(), dir, typeNames)
	},
}
