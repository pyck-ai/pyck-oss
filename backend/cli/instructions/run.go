package instructions

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	trucksCmd.Flags().Int("count", 0, "Number of trucks to run")

	runCmd.AddCommand(trucksCmd)
	runCmd.AddCommand(pickerCmd)
	runCmd.AddCommand(picklistsCmd)
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run actions in the warehouse.",
	Long:  `Execute actions in a warehouse.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("run actions")
	},
}

var picklistsCmd = &cobra.Command{
	Use:   "picklists",
	Short: "Generate picklists",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("generate picklists")
	},
}

var pickerCmd = &cobra.Command{
	Use:   "pickers",
	Short: "Let pickers fullfill picklists",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("let pickers run")
	},
}

var trucksCmd = &cobra.Command{
	Use:   "trucks",
	Short: "Run trucks",
	Args:  cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		count, _ := cmd.Flags().GetInt("count")
		fmt.Printf("run %d trucks\n", count)
	},
}
