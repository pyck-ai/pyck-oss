package instructions

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
)

func init() {
	listCmd.AddCommand(listRepoCmd)
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List entities.",
	Long:  `List entities in the warehouse.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("list entities")
	},
}

func printRepoTree(repoMap map[string]*inventoryapi.GetRepositories_Repositories_Edges_Node, parentID string, level int) {
	for _, repo := range repoMap {
		// Check if ParentID matches - need to handle nil pointer
		var repoParentID string
		if repo.ParentID != nil {
			repoParentID = *repo.ParentID
		}
		if repoParentID == parentID && !repo.VirtualRepo {
			fmt.Print(strings.Repeat("  ", level))
			fmt.Printf("%s - %s virtual: %t\n", repo.Name, repo.ID, repo.VirtualRepo)
			printRepoTree(repoMap, repo.ID, level+1)
		}
	}

	for _, repo := range repoMap {
		var repoParentID string
		if repo.ParentID != nil {
			repoParentID = *repo.ParentID
		}
		if repoParentID == parentID && repo.VirtualRepo {
			fmt.Print(strings.Repeat("  ", level))
			fmt.Printf("%s - %s virtual: %t\n", repo.Name, repo.ID, repo.VirtualRepo)
			printRepoTree(repoMap, repo.ID, level+1)
		}
	}
}

var listRepoCmd = &cobra.Command{
	Use:   "repositories",
	Short: "List repositories",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("List repositories \n\n")

		ctx := context.Background()

		inventoryCli, err := getInventoryClient(cmd)
		if err != nil {
			FailWithError(err)
		}

		repositories := make(map[string]*inventoryapi.GetRepositories_Repositories_Edges_Node)
		first := 100
		var after *string

		for {
			resp, err := inventoryCli.GetRepositories(ctx, inventoryapi.GetRepositoriesArgs{
				After: after,
				First: &first,
			})
			if err != nil {
				FailWithError(err)
			}

			data := resp.GetRepositories()
			for _, edge := range data.Edges {
				if edge.Node != nil {
					repositories[edge.Node.ID] = edge.Node
				}
			}

			if !data.PageInfo.HasNextPage {
				break
			}
			if data.PageInfo.EndCursor != nil {
				after = data.PageInfo.EndCursor
			}
		}

		printRepoTree(repositories, "", 0)
	},
}
