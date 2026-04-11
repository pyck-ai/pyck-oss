package instructions

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/pyck-ai/pyck/backend/common/std"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	pickingmodel "github.com/pyck-ai/pyck/backend/picking/model"
)

var (
	errSupplierDataTypeNotFound     = errors.New("supplier datatype not found")
	errCustomerDataTypeNotFound     = errors.New("customer datatype not found")
	errItemDataTypeNotFound         = errors.New("item datatype not found")
	errRepositoryDataTypeNotFound   = errors.New("repository datatype not found")
	errNoVirtualRepoFound           = errors.New("no virtual repository found to use as source")
	errPickingOrderDataTypeNotFound = errors.New("picking_order datatype not found")
	errNoCustomersFound             = errors.New("no customers found")
	errInvalidRepoType              = errors.New("invalid repository type")
	errNotEnoughItems               = errors.New("not enough items to create orders")
)

func init() {
	dataCmd.Flags().Int("items", 0, "Number of items to create")
	_ = dataCmd.MarkFlagRequired("items")
	dataCmd.Flags().Int("customers", 0, "Number of customers to create")
	_ = dataCmd.MarkFlagRequired("customers")
	dataCmd.Flags().Int("suppliers", 0, "Number of suppliers to create")
	_ = dataCmd.MarkFlagRequired("suppliers")

	repoCmd.Flags().String("type", "haufen", "Type of repositories, default 'haufen'")

	initialStockCmd.Flags().String("target-repo-id", "", "create initial stock in this repo")
	_ = initialStockCmd.MarkFlagRequired("target-repo-id")
	initialStockCmd.Flags().Int("max-items-per-stock", 0, "Maximum possible initial stock for an item")
	_ = initialStockCmd.MarkFlagRequired("max-items-per-stock")
	initialStockCmd.Flags().Int("min-items-per-stock", 1, "Minimum possible initial stock for an item")

	ordersCmd.Flags().Int("count", 0, "Number of orders to create")
	_ = ordersCmd.MarkFlagRequired("count")

	generateCmd.AddCommand(uuidCmd)
	generateCmd.AddCommand(ordersCmd)
	generateCmd.AddCommand(initialStockCmd)
	generateCmd.AddCommand(repoCmd)
	generateCmd.AddCommand(dataCmd)
	rootCmd.AddCommand(generateCmd)
}

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generates stuff in a storage facility.",
	Long:  `Create items, repositories, orders and more.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("generate")
	},
}

var dataCmd = &cobra.Command{
	Use:   "data",
	Short: "Generate data for current tenant",
	Run: func(cmd *cobra.Command, args []string) {
		suppliersCount, _ := cmd.Flags().GetInt("suppliers")
		customersCount, _ := cmd.Flags().GetInt("customers")
		itemsCount, _ := cmd.Flags().GetInt("items")

		ctx := context.Background()

		// Get clients
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

		// Get all datatypes
		fmt.Println("Getting datatypes...")
		dataTypes, err := getAllDataTypes(managementCli, ctx)
		if err != nil {
			FailWithError(err)
		}

		// Get supplier datatype ID
		var supplierDataTypeID uuid.UUID
		if dtMap, ok := dataTypes["supplier"]; ok {
			for _, id := range dtMap {
				supplierDataTypeID = id
				break
			}
		}
		if supplierDataTypeID == uuid.Nil {
			FailWithError(errSupplierDataTypeNotFound)
		}

		// Get customer datatype ID
		var customerDataTypeID uuid.UUID
		if dtMap, ok := dataTypes["customer"]; ok {
			for _, id := range dtMap {
				customerDataTypeID = id
				break
			}
		}
		if customerDataTypeID == uuid.Nil {
			FailWithError(errCustomerDataTypeNotFound)
		}

		// Get item datatype ID
		var itemDataTypeID uuid.UUID
		if dtMap, ok := dataTypes["item"]; ok {
			for _, id := range dtMap {
				itemDataTypeID = id
				break
			}
		}
		if itemDataTypeID == uuid.Nil {
			FailWithError(errItemDataTypeNotFound)
		}

		// Create suppliers
		fmt.Printf("\tCreating %d suppliers ...\n", suppliersCount)
		suppliersCreated := 0
		for range suppliersCount {
			data := map[string]any{
				"name": gofakeit.Company(),
			}
			_, err := mainDataCli.CreateSupplier(ctx, maindataapi.CreateSupplierArgs{
				Input: maindataapi.CreateSupplierInput{
					DataTypeID: &supplierDataTypeID,
					Data:       data,
				},
			})
			if err != nil {
				FailWithError(err)
			}
			suppliersCreated++
		}

		// Create customers
		fmt.Printf("\tCreating %d customers ...\n", customersCount)
		customersCreated := 0
		for range customersCount {
			data := map[string]any{
				"name": gofakeit.Name(),
			}
			_, err := mainDataCli.CreateCustomer(ctx, maindataapi.CreateCustomerArgs{
				Input: maindataapi.CreateCustomerInput{
					DataTypeID: &customerDataTypeID,
					Data:       data,
				},
			})
			if err != nil {
				FailWithError(err)
			}
			customersCreated++
		}

		// Create items
		fmt.Printf("\tCreating %d items ...\n", itemsCount)
		itemsCreated := 0
		for range itemsCount {
			data := map[string]any{
				"name": fmt.Sprintf("%s%s", gofakeit.Company(), std.GenerateRandomString(5)),
			}
			sku := std.GenerateRandomString(15)
			_, err := inventoryCli.CreateInventoryItem(ctx, inventoryapi.CreateInventoryItemArgs{
				Input: inventoryapi.CreateInventoryItemInput{
					DataTypeID: &itemDataTypeID,
					Data:       data,
					Sku:        sku,
				},
			})
			if err != nil {
				FailWithError(err)
			}
			itemsCreated++
		}

		fmt.Println("\nData for current tenant generated:")
		fmt.Printf("\t\t%d suppliers created\n", suppliersCreated)
		fmt.Printf("\t\t%d customers created\n", customersCreated)
		fmt.Printf("\t\t%d items created\n", itemsCreated)
	},
}

var repoCmd = &cobra.Command{
	Use:   "repositories",
	Short: "Generate repositories",
	Run: func(cmd *cobra.Command, args []string) {
		repoType, _ := cmd.Flags().GetString("type")
		if repoType != "haufen" {
			FailWithError(fmt.Errorf("%w: '%s'", errInvalidRepoType, repoType))
		}

		fmt.Printf("Generate repositories with type '%s'.\n", repoType)
		ctx := context.Background()

		inventoryCli, err := getInventoryClient(cmd)
		if err != nil {
			FailWithError(err)
		}
		managementCli, err := getManagementClient(cmd)
		if err != nil {
			FailWithError(err)
		}

		// Check if repositories already exist
		repositories, err := getAllRepositories(inventoryCli, ctx)
		if err != nil {
			FailWithError(err)
		}
		if len(repositories) > 0 {
			fmt.Println("Repositories already exist")
			printRepositoryTree(repositories, "", 0)
			return
		}

		// Get datatypes
		dataTypes, err := getAllDataTypes(managementCli, ctx)
		if err != nil {
			FailWithError(err)
		}

		// Get repository datatype ID
		var repoDataTypeID uuid.UUID
		if dtMap, ok := dataTypes["repository"]; ok {
			for _, id := range dtMap {
				repoDataTypeID = id
				break
			}
		}
		if repoDataTypeID == uuid.Nil {
			FailWithError(errRepositoryDataTypeNotFound)
		}

		virtualFalse := false
		virtualTrue := true

		// Create parent "Halle"
		parent, err := createRepository(inventoryCli, ctx, "Halle", &virtualFalse, &repoDataTypeID, nil)
		if err != nil {
			FailWithError(err)
		}

		// Create Zone A
		zoneA, err := createRepository(inventoryCli, ctx, "Zone A", &virtualFalse, &repoDataTypeID, &parent.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create Wareneingang under Zone A
		_, err = createRepository(inventoryCli, ctx, "Wareneingang", &virtualFalse, &repoDataTypeID, &zoneA.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create Haufen under Zone A
		_, err = createRepository(inventoryCli, ctx, "Haufen", &virtualFalse, &repoDataTypeID, &zoneA.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create Zone B
		zoneB, err := createRepository(inventoryCli, ctx, "Zone B", &virtualFalse, &repoDataTypeID, &parent.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create Warenausgang under Zone B
		_, err = createRepository(inventoryCli, ctx, "Warenausgang", &virtualFalse, &repoDataTypeID, &zoneB.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create Buffer under Zone B
		_, err = createRepository(inventoryCli, ctx, "Buffer", &virtualFalse, &repoDataTypeID, &zoneB.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create virtual parent "Virtual Halle"
		virtualParent, err := createRepository(inventoryCli, ctx, "Virtual Halle", &virtualTrue, &repoDataTypeID, nil)
		if err != nil {
			FailWithError(err)
		}

		// Create Virtual Container C
		_, err = createRepository(inventoryCli, ctx, "Virtual Container C", &virtualTrue, &repoDataTypeID, &virtualParent.ID)
		if err != nil {
			FailWithError(err)
		}

		// Create Virtual Container D
		_, err = createRepository(inventoryCli, ctx, "Virtual Container D", &virtualTrue, &repoDataTypeID, &virtualParent.ID)
		if err != nil {
			FailWithError(err)
		}

		// Fetch and print repository tree
		repositories, err = getAllRepositories(inventoryCli, ctx)
		if err != nil {
			FailWithError(err)
		}
		printRepositoryTree(repositories, "", 0)
	},
}

var initialStockCmd = &cobra.Command{
	Use:   "initial-stock",
	Short: "Generate initial stocks",
	Run: func(cmd *cobra.Command, args []string) {
		targetRepoIDStr, _ := cmd.Flags().GetString("target-repo-id")
		targetRepoID, err := uuid.Parse(targetRepoIDStr)
		if err != nil {
			FailWithError(err)
		}
		minItemsPerStock, _ := cmd.Flags().GetInt("min-items-per-stock")
		maxItemsPerStock, _ := cmd.Flags().GetInt("max-items-per-stock")

		fmt.Printf("Generate initial stocks for current tenant, moving to repo %s [max-stock: %d]\n", targetRepoID, maxItemsPerStock)

		ctx := context.Background()

		inventoryCli, err := getInventoryClient(cmd)
		if err != nil {
			FailWithError(err)
		}

		// Get all items
		allItems, err := getAllItems(inventoryCli, ctx)
		if err != nil {
			FailWithError(err)
		}

		// Get all repositories
		repositories, err := getAllRepositories(inventoryCli, ctx)
		if err != nil {
			FailWithError(err)
		}

		// Find a virtual repository to use as source
		var sourceRepoID string
		for _, repo := range repositories {
			if repo.VirtualRepo {
				fmt.Printf("Moving initial stock from source repo %s - %s\n", repo.Name, repo.ID)
				sourceRepoID = repo.ID
				break
			}
		}

		if sourceRepoID == "" {
			FailWithError(errNoVirtualRepoFound)
		}

		fmt.Printf("Found %d items for tenant.\n", len(allItems))
		for _, item := range allItems {
			quantity := rand.Intn(maxItemsPerStock-minItemsPerStock) + minItemsPerStock

			// Create item movement
			movement, err := inventoryCli.CreateInventoryItemMovement(ctx, inventoryapi.CreateInventoryItemMovementArgs{
				Input: inventoryapi.CreateItemMovementInput{
					ItemID:   item.ID,
					FromID:   sourceRepoID,
					ToID:     targetRepoID.String(),
					Quantity: quantity,
					Handler:  "manual",
				},
			})
			if err != nil {
				FailWithError(fmt.Errorf("failed to create movement for item %s: %w", item.ID, err))
			}

			// Execute the movement
			movementData := movement.GetCreateInventoryItemMovement().GetInventoryItemMovement()
			_, err = inventoryCli.ExecuteInventoryItemMovement(ctx, inventoryapi.ExecuteInventoryItemMovementArgs{
				Id: movementData.ID,
			})
			if err != nil {
				FailWithError(fmt.Errorf("failed to execute movement %s: %w", movementData.ID, err))
			}

			fmt.Printf("Created and executed movement %s for item %s with quantity %d\n", movementData.ID, item.ID, quantity)
		}
		fmt.Println("Movements executed.")
	},
}

var ordersCmd = &cobra.Command{
	Use:   "orders",
	Short: "Generate orders",
	Run: func(cmd *cobra.Command, args []string) {
		count, _ := cmd.Flags().GetInt("count")

		fmt.Printf("Generate %d orders \n", count)
		ctx := context.Background()

		// Get clients
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

		// Get datatypes
		dataTypes, err := getAllDataTypes(managementCli, ctx)
		if err != nil {
			FailWithError(err)
		}

		// Get picking_order datatype ID
		var orderDataTypeID uuid.UUID
		if dtMap, ok := dataTypes["picking_order"]; ok {
			for _, id := range dtMap {
				orderDataTypeID = id
				break
			}
		}
		if orderDataTypeID == uuid.Nil {
			FailWithError(errPickingOrderDataTypeNotFound)
		}

		// Get customers
		customers, err := getAllCustomers(mainDataCli, ctx)
		if err != nil {
			FailWithError(err)
		}
		fmt.Println("Customers:", len(customers))

		// Get all items
		allItems, err := getAllItems(inventoryCli, ctx)
		if err != nil {
			FailWithError(err)
		}
		fmt.Println("Items:", len(allItems))

		if len(allItems) < 2 {
			FailWithError(fmt.Errorf("%w (need at least 2, have %d)", errNotEnoughItems, len(allItems)))
		}

		if len(customers) == 0 {
			FailWithError(errNoCustomersFound)
		}

		fmt.Println("Creating orders")
		for i := 0; i < count; i++ {
			orderItems := []*pickingmodel.CreatePickingOrderItemsInput{}

			// Add 2 random items to each order
			for range 2 {
				//nolint:gosec // G404: math/rand is sufficient for test data generation
				quantity := rand.Intn(4) + 1 // 1-5
				item := getRandomEntry(allItems)
				orderItems = append(orderItems, &pickingmodel.CreatePickingOrderItemsInput{
					Sku:      item.Sku,
					Quantity: quantity,
				})
			}

			// Select random customer
			customer := getRandomEntry(customers)
			customerID, err := uuid.Parse(customer.ID)
			if err != nil {
				FailWithError(fmt.Errorf("failed to parse customer ID %s: %w", customer.ID, err))
			}

			// Create order
			order, err := pickingCli.CreatePickingOrder(ctx, pickingapi.CreatePickingOrderArgs{
				Input: pickingmodel.CreatePickingOrderWithItemsInput{
					CustomerID: &customerID,
					DataTypeID: &orderDataTypeID,
					OrderItems: orderItems,
				},
			})
			if err != nil {
				FailWithError(fmt.Errorf("failed to create order: %w", err))
			}

			orderData := order.GetCreatePickingOrder().GetPickingOrder()
			fmt.Printf("Created order %s\n", orderData.ID)
		}
		fmt.Println("Orders created.")
	},
}

var uuidCmd = &cobra.Command{
	Use:   "uuid",
	Short: "Generate uuid from given string argument, for example from org-id or user-id",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(uuid.NewSHA1(uuid.NameSpaceOID, []byte(args[0])))
	},
}

// getAllDataTypes retrieves all datatypes from the management service
func getAllDataTypes(client managementapi.Client, ctx context.Context) (map[string]map[string]uuid.UUID, error) {
	dataTypesIds := make(map[string]map[string]uuid.UUID)
	first := 100
	var after *string

	for {
		resp, err := client.GetDataTypes(ctx, managementapi.GetDataTypesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, err
		}

		data := resp.GetDataTypes()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				entity := edge.Node.Entity
				if _, ok := dataTypesIds[entity]; !ok {
					dataTypesIds[entity] = make(map[string]uuid.UUID)
				}
				entityID, err := uuid.Parse(edge.Node.ID)
				if err != nil {
					return nil, fmt.Errorf("failed to parse datatype ID %s: %w", edge.Node.ID, err)
				}
				dataTypesIds[entity][edge.Node.Name] = entityID
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		after = data.PageInfo.EndCursor
	}

	return dataTypesIds, nil
}

// getAllCustomers retrieves all customers from the main-data service
func getAllCustomers(client maindataapi.Client, ctx context.Context) ([]*maindataapi.GetCustomers_Customers_Edges_Node, error) {
	var customers []*maindataapi.GetCustomers_Customers_Edges_Node
	first := 100
	var after *string

	for {
		resp, err := client.GetCustomers(ctx, maindataapi.GetCustomersArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, err
		}

		data := resp.GetCustomers()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				customers = append(customers, edge.Node)
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		after = data.PageInfo.EndCursor
	}

	return customers, nil
}

// getAllItems retrieves all inventory items from the inventory service
func getAllItems(client inventoryapi.Client, ctx context.Context) ([]*inventoryapi.GetInventoryItems_InventoryItems_Edges_Node, error) {
	var items []*inventoryapi.GetInventoryItems_InventoryItems_Edges_Node
	first := 100
	var after *string

	for {
		resp, err := client.GetInventoryItems(ctx, inventoryapi.GetInventoryItemsArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, err
		}

		data := resp.GetInventoryItems()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				items = append(items, edge.Node)
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		after = data.PageInfo.EndCursor
	}

	return items, nil
}

// getAllRepositories retrieves all repositories from the inventory service
func getAllRepositories(client inventoryapi.Client, ctx context.Context) ([]*inventoryapi.GetRepositories_Repositories_Edges_Node, error) {
	var repositories []*inventoryapi.GetRepositories_Repositories_Edges_Node
	first := 100
	var after *string

	for {
		resp, err := client.GetRepositories(ctx, inventoryapi.GetRepositoriesArgs{
			After: after,
			First: &first,
		})
		if err != nil {
			return nil, err
		}

		data := resp.GetRepositories()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				repositories = append(repositories, edge.Node)
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		after = data.PageInfo.EndCursor
	}

	return repositories, nil
}

// createRepository creates a single repository
func createRepository(client inventoryapi.Client, ctx context.Context, name string, virtual *bool, dataTypeID *uuid.UUID, parentID *string) (*inventoryapi.CreateInventoryRepository_CreateInventoryRepository_InventoryRepository, error) {
	data := map[string]any{
		"name": name,
	}

	resp, err := client.CreateInventoryRepository(ctx, inventoryapi.CreateInventoryRepositoryArgs{
		Input: inventoryapi.CreateRepositoryInput{
			Name:        name,
			Type:        entrepository.TypeStatic,
			VirtualRepo: virtual,
			DataTypeID:  dataTypeID,
			Data:        data,
			ParentID:    parentID,
		},
	})
	if err != nil {
		return nil, err
	}

	return resp.GetCreateInventoryRepository().GetInventoryRepository(), nil
}

// printRepositoryTree prints the repository hierarchy recursively
func printRepositoryTree(repositories []*inventoryapi.GetRepositories_Repositories_Edges_Node, parentID string, level int) {
	for _, repo := range repositories {
		// Handle nullable ParentID
		var repoParentID string
		if repo.ParentID != nil {
			repoParentID = *repo.ParentID
		}
		if repoParentID == parentID && !repo.VirtualRepo {
			fmt.Print(strings.Repeat("  ", level))
			fmt.Printf("%s - %s virtual: %t\n", repo.Name, repo.ID, repo.VirtualRepo)
			printRepositoryTree(repositories, repo.ID, level+1)
		}
	}
}
