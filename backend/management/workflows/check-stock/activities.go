package checkstock

import (
	"context"
	"time"

	apiclient "github.com/pyck-ai/pyck/backend/common/services/api_client"
)

func ComputeTimeRangesActivity(ctx context.Context) (*TimeRange, error) {
	now := time.Now()
	startOfDay := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	endOfDay := startOfDay.Add(24 * time.Hour)
	endOfYesterday := startOfDay.Add(-time.Second)

	return &TimeRange{
		StartOfDay:     startOfDay,
		EndOfDay:       endOfDay,
		EndOfYesterday: endOfYesterday,
	}, nil
}

func FetchTransactionsActivity(ctx context.Context, input FetchTransactionsInput) ([]Transaction, error) {
	var response []Transaction

	client := apiclient.NewAPIClient(input.ApiURL, input.AuthToken)
	transactionList, err := client.GetTransactionsByTimeRange(ctx, input.TimeRange.StartOfDay, input.TimeRange.EndOfDay)
	if err != nil {
		return nil, err
	}

	for _, v := range transactionList {
		response = append(response, Transaction{
			ItemID:       v.ItemID,
			RepositoryID: v.RepositoryID,
			Quantity:     v.Quantity,
			Type:         v.Type,
		})
	}

	return response, nil
}

func ComputeExpectedStockActivity(ctx context.Context, input ComputeExpectedStockInput) ([]StockExpectation, error) {
	apiClient := apiclient.NewAPIClient(input.ApiURL, input.AuthToken)
	initialStock := make(map[StockKey]int)
	var response []StockExpectation

	for _, tx := range input.Transactions {
		k := StockKey{ItemID: tx.ItemID, RepositoryID: tx.RepositoryID}

		if _, ok := initialStock[k]; !ok {
			stock, err := apiClient.GetStockByItemAndRepository(ctx, k.ItemID, k.RepositoryID, &input.EndOfYesterday)
			if err != nil {
				initialStock[k] = 0
			} else {
				initialStock[k] = stock.Quantity
			}
		}

		if tx.Type == "into" {
			initialStock[k] += tx.Quantity
		} else {
			initialStock[k] -= tx.Quantity
		}
	}

	for k, v := range initialStock {
		response = append(response, StockExpectation{
			ItemID:       k.ItemID,
			RepositoryID: k.RepositoryID,
			Expected:     v,
		})
	}

	return response, nil
}

func CompareActualStockActivity(ctx context.Context, input CompareStockInput) ([]Discrepancy, error) {
	var response []Discrepancy

	apiClient := apiclient.NewAPIClient(input.ApiURL, input.AuthToken)
	for _, exp := range input.Expectations {
		stock, err := apiClient.GetStockByItemAndRepository(ctx, exp.ItemID, exp.RepositoryID, &input.EndOfDay)
		if err != nil {
			continue
		}
		if stock.Quantity != exp.Expected {
			response = append(response, Discrepancy{
				ItemID:       exp.ItemID,
				RepositoryID: exp.RepositoryID,
				Expected:     exp.Expected,
				Actual:       stock.Quantity,
			})
		}
	}

	return response, nil
}
