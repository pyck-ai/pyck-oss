package instructions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/pyck-ai/pyck/backend/common/env"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
)

var errStockNotFound = errors.New("stock not found")

func init() {
	checkCmd.PersistentFlags().String(authTokenFlagName, "", "Auth token of Graphql-API. [PYCK_AUTH]")
	_ = viper.BindPFlag("AUTH", checkCmd.PersistentFlags().Lookup(authTokenFlagName))

	checkCmd.PersistentFlags().String(envNameFlag, "local", "Environment, for example dev")
	_ = viper.BindPFlag(envNameFlag, checkCmd.PersistentFlags().Lookup(envNameFlag))

	checkCmd.PersistentFlags().String(envPathFlag, "environments.yaml", "Environment file path, default environments.yaml")
	_ = viper.BindPFlag(envPathFlag, checkCmd.PersistentFlags().Lookup(envPathFlag))

	checkCmd.AddCommand(checkStockCmd)
	rootCmd.AddCommand(checkCmd)
}

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check.",
	Long:  `Check.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("check")
	},
}

var checkStockCmd = &cobra.Command{
	Use:   "stock",
	Short: "Check consistency between transactions and stocks for today",
	Run: func(cmd *cobra.Command, args []string) {
		authToken := viper.GetString("AUTH")
		commandToken, _ := cmd.Flags().GetString(authTokenFlagName)
		if len(commandToken) > 0 {
			authToken = commandToken
		}

		if authToken == "" {
			FailWithError(fmt.Errorf("--%s [PYCK_AUTH] is missing.", authTokenFlagName))
		}

		envName, _ := cmd.Flags().GetString(envNameFlag)
		envPath, _ := cmd.Flags().GetString(envPathFlag)
		envs, err := env.ReadYamlEnv(envPath, envName)

		apiUrl, ok := envs[apiUrlEnvName]
		if !ok {
			FailWithError(fmt.Errorf("api url not found in environment: %s", envName))
		}

		interceptor := &clientAuthInterceptor{token: authToken}
		inventoryCli := inventoryapi.NewClient(http.DefaultClient, apiUrl, nil, interceptor.intercept)
		ctx := context.Background()

		// Define time ranges
		startOfDay := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
		endOfDay := startOfDay.Add(24 * time.Hour)
		endOfYesterday := startOfDay.Add(-time.Second)

		fmt.Printf("Checking consistency for transactions from %s to %s\n\n", startOfDay.Format(time.RFC3339), endOfDay.Format(time.RFC3339))

		// Fetch transactions within time range using where filter
		transactions, err := getAllTransactionsByTimeRange(inventoryCli, ctx, startOfDay, endOfDay)
		if err != nil {
			FailWithError(fmt.Errorf("failed to fetch transactions: %w", err))
		}

		type key struct {
			ItemID       string
			RepositoryID string
		}
		sums := make(map[key]int64)
		initialStock := make(map[key]int64)

		for _, tx := range transactions {
			k := key{ItemID: tx.ItemID, RepositoryID: tx.RepositoryID}

			if _, ok := initialStock[k]; !ok {
				stock, err := getStockByItemAndRepository(inventoryCli, ctx, k.ItemID, k.RepositoryID, &endOfYesterday)
				if err != nil {
					initialStock[k] = 0
				} else {
					initialStock[k] = int64(stock.Quantity)
				}
				sums[k] = initialStock[k]
			}

			if tx.Type == "into" {
				sums[k] += int64(tx.Quantity)
			} else {
				sums[k] -= int64(tx.Quantity)
			}
		}

		totalChecked := 0
		totalDiscrepancies := 0

		for k, expectedQty := range sums {
			stock, err := getStockByItemAndRepository(inventoryCli, ctx, k.ItemID, k.RepositoryID, &endOfDay)
			if err != nil {
				fmt.Printf("Could not retrieve stock for item %s | repository %s: %s\n", k.ItemID, k.RepositoryID, err.Error())
				continue
			}

			if diff := int64(stock.Quantity) - expectedQty; diff != 0 {
				totalDiscrepancies++
				fmt.Printf("Mismatch: item %s | repository %s — stock: %d vs expected: %d\n", k.ItemID, k.RepositoryID, stock.Quantity, expectedQty)
			} else {
				fmt.Printf("Match: item %s | repository %s — stock: %d vs expected: %d\n", k.ItemID, k.RepositoryID, stock.Quantity, expectedQty)
			}
			totalChecked++
		}

		fmt.Printf("\nChecked %d item-repository pairs.\n", totalChecked)
		if totalDiscrepancies == 0 {
			fmt.Println("No discrepancies found.")
		} else {
			fmt.Printf("Found %d discrepancies.\n", totalDiscrepancies)
		}
	},
}

func getAllTransactionsByTimeRange(client inventoryapi.Client, ctx context.Context, startTime, endTime time.Time) ([]*inventoryapi.GetTransactions_Transactions_Edges_Node, error) {
	var transactions []*inventoryapi.GetTransactions_Transactions_Edges_Node
	first := 100
	var after *string

	where := &inventoryapi.TransactionWhereInput{
		CreatedAtGte: &startTime,
		CreatedAtLt:  &endTime,
	}

	for {
		resp, err := client.GetTransactions(ctx, inventoryapi.GetTransactionsArgs{
			After: after,
			First: &first,
			Where: where,
		})
		if err != nil {
			return nil, err
		}

		data := resp.GetTransactions()
		for _, edge := range data.Edges {
			if edge.Node != nil {
				transactions = append(transactions, edge.Node)
			}
		}

		if !data.PageInfo.HasNextPage {
			break
		}
		if data.PageInfo.EndCursor != nil {
			after = data.PageInfo.EndCursor
		}
	}

	return transactions, nil
}

func getStockByItemAndRepository(client inventoryapi.Client, ctx context.Context, itemID, repositoryID string, asOfTime *time.Time) (*inventoryapi.GetStocks_Stocks_Edges_Node, error) {
	first := 1

	whereInput := &inventoryapi.StockWhereInput{
		ItemID:       &itemID,
		RepositoryID: &repositoryID,
	}
	if asOfTime != nil {
		whereInput.CreatedAtLte = asOfTime
	}

	resp, err := client.GetStocks(ctx, inventoryapi.GetStocksArgs{
		First: &first,
		Where: whereInput,
	})
	if err != nil {
		return nil, err
	}

	data := resp.GetStocks()
	if len(data.Edges) == 0 {
		return nil, errStockNotFound
	}

	return data.Edges[0].Node, nil
}
