package instructions

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
)

func init() {
	rootCmd.AddCommand(cleanCmd)
}

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Clean data for current tenant",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Cleaning all tenant data...")
		ctx := context.Background()

		// Get all clients
		mainDataCli, err := getMainDataClient(cmd)
		if err != nil {
			FailWithError(err)
		}
		inventoryCli, err := getInventoryClient(cmd)
		if err != nil {
			FailWithError(err)
		}
		managementCli, err := getManagementClient(cmd)
		if err != nil {
			FailWithError(err)
		}
		pickingCli, err := getPickingClient(cmd)
		if err != nil {
			FailWithError(err)
		}

		// Delete picking orders and items first (top of dependency chain)
		fmt.Println("Deleting picking order items...")
		if err := deleteAllPickingOrderItems(pickingCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete picking order items: %v\n", err)
		}

		fmt.Println("Deleting picking orders...")
		if err := deleteAllPickingOrders(pickingCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete picking orders: %v\n", err)
		}

		// Delete inventory-related entities
		fmt.Println("Deleting inventory item movements...")
		if err := deleteAllItemMovements(inventoryCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete item movements: %v\n", err)
		}

		fmt.Println("Deleting inventory items...")
		if err := deleteAllInventoryItems(inventoryCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete inventory items: %v\n", err)
		}

		fmt.Println("Deleting repositories...")
		if err := deleteAllRepositories(inventoryCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete repositories: %v\n", err)
		}

		// Delete main data entities
		fmt.Println("Deleting customers...")
		if err := deleteAllCustomers(mainDataCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete customers: %v\n", err)
		}

		fmt.Println("Deleting suppliers...")
		if err := deleteAllSuppliers(mainDataCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete suppliers: %v\n", err)
		}

		// Delete datatypes last
		fmt.Println("Deleting data types...")
		if err := deleteAllDataTypes(managementCli, ctx); err != nil {
			fmt.Printf("Warning: failed to delete data types: %v\n", err)
		}

		fmt.Println("\nClean completed")
	},
}

// Helper functions to delete all entities with pagination

func deleteAllPickingOrderItems(client pickingapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetPickingOrderItems(ctx, pickingapi.GetPickingOrderItemsArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetPickingOrderItems()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeletePickingOrderItem(ctx, pickingapi.DeletePickingOrderItemArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete picking order item %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d picking order items\n", deletedCount)
	return nil
}

func deleteAllPickingOrders(client pickingapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetPickingOrders(ctx, pickingapi.GetPickingOrdersArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetPickingOrders()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeletePickingOrder(ctx, pickingapi.DeletePickingOrderArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete picking order %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d picking orders\n", deletedCount)
	return nil
}

func deleteAllItemMovements(client inventoryapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetItemMovements(ctx, inventoryapi.GetItemMovementsArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetItemMovements()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeleteInventoryItemMovement(ctx, inventoryapi.DeleteInventoryItemMovementArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete item movement %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		data = resp.GetItemMovements()
		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d item movements\n", deletedCount)
	return nil
}

func deleteAllInventoryItems(client inventoryapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetInventoryItems(ctx, inventoryapi.GetInventoryItemsArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetInventoryItems()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeleteInventoryItem(ctx, inventoryapi.DeleteInventoryItemArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete inventory item %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d inventory items\n", deletedCount)
	return nil
}

type repoNode struct {
	ID       string
	ParentID string
}

func deleteAllRepositories(client inventoryapi.Client, ctx context.Context) error {
	// Get all repositories first
	var repositories []repoNode
	var after *string
	first := 100

	for {
		resp, err := client.GetRepositories(ctx, inventoryapi.GetRepositoriesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetRepositories()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				var parentID string
				if edge.Node.ParentID != nil {
					parentID = *edge.Node.ParentID
				}
				repositories = append(repositories, repoNode{
					ID:       edge.Node.ID,
					ParentID: parentID,
				})
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	// Delete repositories recursively (children first)
	deletedCount := deleteRepositoriesRecursive(client, ctx, "", repositories)
	fmt.Printf("  Deleted %d repositories\n", deletedCount)
	return nil
}

func deleteRepositoriesRecursive(client inventoryapi.Client, ctx context.Context, parentID string, allRepos []repoNode) int {
	deletedCount := 0

	// Find children of this parent
	for _, repo := range allRepos {
		if repo.ParentID == parentID {
			// Recursively delete children first
			deletedCount += deleteRepositoriesRecursive(client, ctx, repo.ID, allRepos)

			// Delete this repository
			_, err := client.DeleteInventoryRepository(ctx, inventoryapi.DeleteInventoryRepositoryArgs{
				Id: repo.ID,
			})
			if err != nil {
				fmt.Printf("  Warning: failed to delete repository %s: %v\n", repo.ID, err)
			} else {
				deletedCount++
			}
		}
	}

	return deletedCount
}

func deleteAllCustomers(client maindataapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetCustomers(ctx, maindataapi.GetCustomersArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetCustomers()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeleteCustomer(ctx, maindataapi.DeleteCustomerArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete customer %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d customers\n", deletedCount)
	return nil
}

func deleteAllSuppliers(client maindataapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetSuppliers(ctx, maindataapi.GetSuppliersArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetSuppliers()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeleteSupplier(ctx, maindataapi.DeleteSupplierArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete supplier %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d suppliers\n", deletedCount)
	return nil
}

func deleteAllDataTypes(client managementapi.Client, ctx context.Context) error {
	var after *string
	first := 100
	deletedCount := 0

	for {
		resp, err := client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return err
		}

		data := resp.GetDataTypes()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				_, err := client.DeleteDataType(ctx, managementapi.DeleteDataTypeArgs{
					Id: edge.Node.ID,
				})
				if err != nil {
					fmt.Printf("  Warning: failed to delete datatype %s: %v\n", edge.Node.ID, err)
				} else {
					deletedCount++
				}
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	fmt.Printf("  Deleted %d datatypes\n", deletedCount)
	return nil
}
