package instructions

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of pyck",
	Long:  `All software has versions. This is pycks's`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("pycl cli tool v0.1 -- HEAD")
	},
}
