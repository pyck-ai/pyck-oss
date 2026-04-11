// Package apiclient provides a GraphQL API client for the pyck API.
//
// Deprecated: This package is deprecated and will be removed in a future version.
// Use the API interfaces from each individual service instead. Each service
// (management, inventory, workflow, etc.) should provide its own API client
// or interface for direct communication.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/google/uuid"
	"github.com/pyck-ai/pyck/backend/common/datatype"
	"github.com/pyck-ai/pyck/backend/common/std"
)

// ReadResponseData reads the HTTP response and unmarshals it into a generic type.
func ReadResponseData[T any](resp *http.Response) (T, error) {
	body, _ := io.ReadAll(resp.Body)
	// fmt.Println(string(body))
	var result T
	err := json.Unmarshal(body, &result)
	return result, err
}

// ApiClient holds the HTTP client configuration and credentials.
//
// Deprecated: Use service-specific API interfaces instead.
type ApiClient struct {
	httpClient *http.Client
	gatewayURL string
	token      string
}

// NewAPIClient returns Graphql client for the pyck API.
//
// Deprecated: Use service-specific API interfaces instead.
func NewAPIClient(gatewayURL, token string) *ApiClient {
	return &ApiClient{
		httpClient: &http.Client{
			Timeout: time.Second * 10,
		},
		gatewayURL: gatewayURL,
		token:      token,
	}
}

// DoRequest sends a GraphQL request to the configured gateway with auth.
func (a *ApiClient) DoRequest(ctx context.Context, requestGQ string) (*http.Response, error) {
	payload, _ := json.Marshal(ApiRequest{Query: requestGQ})
	req, err := http.NewRequest(http.MethodPost, a.gatewayURL, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Authorization", "Bearer "+a.token)

	return a.httpClient.Do(req)
}

// CreateDefaultDataTypes Create default data types.
func (a *ApiClient) CreateDefaultDataTypes(ctx context.Context) (map[string]*DataType, error) {
	dataTypeEntities := datatype.DataTypeEntities()
	dataTypes := make(map[string]*DataType, len(dataTypeEntities))
	jsonSchema := `"{\n\"type\": \"object\",\n\"required\": [\"name\"],\n\"properties\": {\n\"name\": {\n\"type\": \"string\"\n}\n}\n}"`
	for _, entity := range dataTypeEntities {
		newDataType, err := a.CreateDataTypes(ctx, true, entity, jsonSchema, 1)
		if err != nil {
			return nil, err
		}
		dataTypes[entity] = newDataType[0]
	}
	return dataTypes, nil
}

// GetDefaultDataTypes Get default data types.
func (a *ApiClient) GetDefaultDataTypes(ctx context.Context) (map[string]DataType, error) {
	mutationStr := `query DataType {
	  dataTypes(
		first: 20
		after: %s,
		where: {
		  default:true
		}
	) {
		totalCount
		edges {
		  node {
			id
			jsonSchema
			entity
			default
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	result := make(map[string]DataType, 0)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*dataTypesQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.DataTypes.Edges {
			result[edge.Node.Entity] = edge.Node
		}
		// handle paging
		nextPage = data.Data.DataTypes.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.DataTypes.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// GetAllDataTypes Get all data types.
func (a *ApiClient) GetAllDataTypes(ctx context.Context) (map[uuid.UUID]DataType, error) {
	mutationStr := `query DataType {
	  dataTypes(
		first: 20
		after: %s,
	) {
		totalCount
		edges {
		  node {
			id
			name
			description
			jsonSchema
			entity
			default
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	result := make(map[uuid.UUID]DataType)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*dataTypesQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.DataTypes.Edges {
			result[edge.Node.ID] = edge.Node
		}
		// handle paging
		nextPage = data.Data.DataTypes.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.DataTypes.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// CreateDataType Create data type.
func (a *ApiClient) CreateDataType(ctx context.Context, name string, description string, isDefault bool, entity string, jsonSchema string) (*DataType, error) {
	mutationStr := `mutation {
		createDataType(input: {
		  name: "%s",
		  description: "%s",
		  default: %t,
		  entity: "%s",
		  jsonSchema: %s
		}) {
			id
			name
			description
			default
			entity
			jsonSchema
		}
	}`

	mutation := fmt.Sprintf(mutationStr, name, description, isDefault, entity, jsonSchema)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*dataTypeCreateResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		errorString := ""
		for _, apiErr := range data.Errors {
			errorString += fmt.Sprintf("%s\n", apiErr.Message)
		}

		return nil, errors.New(errorString)
	}

	if (data.Data.CreateDataType.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid datatype ID returned, %s", data.Data.CreateDataType.ID)
	}

	return &data.Data.CreateDataType, nil
}

// CreateDataTypes Create data types.
func (a *ApiClient) CreateDataTypes(ctx context.Context, isDefault bool, entity string, jsonSchema string, count int) ([]*DataType, error) {
	result := []*DataType{}
	for i := 0; i < count; i++ {
		data, err := a.CreateDataType(ctx, fmt.Sprintf("%s %s %d", entity, std.GenerateRandomString(3), i), "Description", isDefault, entity, jsonSchema)
		if err != nil {
			return nil, err
		}
		result = append(result, data)
	}
	return result, nil
}

// UpdateDataType Update data type.
func (a *ApiClient) UpdateDataType(ctx context.Context, ID uuid.UUID, name string, description string, isDefault bool, jsonSchema string) (*DataType, error) {
	mutationStr := `mutation UpdateDataType {
		updateDataType(
			id: "%s"
			input: { name: "%s", description: "%s", default: %t, jsonSchema: %s }
		) {
			id
			name
			description
			default
			entity
			jsonSchema
		}
	}`

	mutation := fmt.Sprintf(mutationStr, ID, name, description, isDefault, jsonSchema)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*dataTypeUpdateResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		errorString := ""
		for _, apiErr := range data.Errors {
			errorString += fmt.Sprintf("%s\n", apiErr.Message)
		}

		return nil, errors.New(errorString)
	}

	if (data.Data.UpdateDataType.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid datatype ID returned, %s", data.Data.UpdateDataType.ID)
	}

	return &data.Data.UpdateDataType, nil
}

// DeleteDataType Delete data type.
func (a *ApiClient) DeleteDataType(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteDataTypeMutation {
	  deleteDataType(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*dataTypeDeleteResponse](resp)
	return err
}

// CreateSuppliers Create suppliers.
func (a *ApiClient) CreateSuppliers(ctx context.Context, dataType *DataType, count int) ([]*Supplier, error) {
	mutationStr := `mutation CreateSupplier {
		createSupplier(input: {
		  dataTypeID:"%s",
		  data: {
			name: "%s"
		  }
		}) {
			id
		}
	}`
	result := []*Supplier{}
	for i := 0; i < count; i++ {
		mutation := fmt.Sprintf(mutationStr, dataType.ID, gofakeit.Company())
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*supplierCreateResponse](resp)
		if err != nil {
			return nil, err
		}

		if (data.Data.CreateSupplier.ID == uuid.UUID{}) {
			return nil, fmt.Errorf("invalid supplier ID returned, %s", data.Data.CreateSupplier.ID)
		}
		result = append(result, &data.Data.CreateSupplier)
	}
	return result, nil
}

// CreateCustomers Create customers.
func (a *ApiClient) CreateCustomers(ctx context.Context, dataType *DataType, count int) ([]*Customer, error) {
	mutationStr := `mutation CreateCustomer {
		createCustomer(input: {
		  dataTypeID:"%s",
		  data: {
			name: "%s"
		  }
		}) {
			id
		}
	}`
	result := []*Customer{}
	for i := 0; i < count; i++ {
		mutation := fmt.Sprintf(mutationStr, dataType.ID, gofakeit.CelebrityBusiness())
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*customerCreateResponse](resp)
		if err != nil {
			return nil, err
		}

		if (data.Data.CreateCustomer.ID == uuid.UUID{}) {
			return nil, fmt.Errorf("invalid customers ID returned, %s", data.Data.CreateCustomer.ID)
		}
		result = append(result, &data.Data.CreateCustomer)
	}
	return result, nil
}

// GetCustomers Get customers.
func (a *ApiClient) GetCustomers(ctx context.Context) ([]Customer, error) {
	mutationStr := `query {
	  customers (
		first: 100
		after: %s
	) {
		totalCount
		edges {
		  node {
			id
			data
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	result := []Customer{}
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*customersQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.Customers.Edges {
			result = append(result, edge.Node)
		}
		// handle paging
		nextPage = data.Data.Customers.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Customers.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// DeleteCustomer Delete customer.
func (a *ApiClient) DeleteCustomer(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteCustomer {
	  deleteCustomer(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*deleteCustomerResponse](resp)

	return err
}

// DeleteSupplier Delete supplier.
func (a *ApiClient) DeleteSupplier(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteSupplier {
	  deleteSupplier(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*deleteSupplierResponse](resp)

	return err
}

// GetSuppliers Get suppliers.
func (a *ApiClient) GetSuppliers(ctx context.Context) ([]Supplier, error) {
	mutationStr := `query {
	  suppliers (
		first: 100
		after: %s
	) {
		totalCount
		edges {
		  node {
			id
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	result := []Supplier{}
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*supplierQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.Suppliers.Edges {
			result = append(result, edge.Node)
		}
		// handle paging
		nextPage = data.Data.Suppliers.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Suppliers.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// CreateInventoryItems Create inventory items.
func (a *ApiClient) CreateInventoryItems(ctx context.Context, dataType *DataType, data *string, sku string, count int) ([]*InventoryItem, error) {
	dataValue := "null"
	if data != nil {
		dataValue = *data
	}

	mutationStr := `mutation CreateInventoryItem {
		createInventoryItem(input: {
		  sku: "%s",
          dataTypeID: "%s",
		  data: %s
		}) {
			inventoryItem {
				id
				sku
			}
		}}`
	var result []*InventoryItem
	for i := 0; i < count; i++ {
		mutationSku := sku
		if i > 0 {
			mutationSku = fmt.Sprintf("%s_%d", sku, i)
		}

		mutation := fmt.Sprintf(mutationStr, mutationSku, dataType.ID, dataValue)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryItemCreateResponse](resp)
		if err != nil {
			return nil, err
		}

		if (data.Data.CreateInventoryItem.InventoryItem.ID == uuid.UUID{}) {
			return nil, fmt.Errorf("invalid inventory item ID returned, %s", data.Data.CreateInventoryItem.InventoryItem.ID)
		}
		result = append(result, &data.Data.CreateInventoryItem.InventoryItem)
	}
	return result, nil
}

// GetItemsWithFilter Get items with filter.
func (a *ApiClient) GetItemsWithFilter(ctx context.Context, whereFilter string) ([]InventoryItem, error) {
	mutationStr := `query QueryItemsWithFilter {
	inventoryItems(first:100,
	      after: %s,
		  orderBy: { direction: ASC, field: CREATED_AT },
		  where: %s
		) {
		  totalCount
		  edges {
			node {
			  	id
				sku
				data

			}
		 }
		 pageInfo {
		   hasPreviousPage
		   startCursor
		   endCursor
		 }
	  }
	}`

	var items []InventoryItem
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor, whereFilter)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryItemQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		if len(data.Errors) > 0 {
			return nil, errors.New(data.Errors[0].Message)
		}

		for _, edge := range data.Data.InventoryItems.Edges {
			items = append(items, edge.Node)
		}

		nextPage = data.Data.InventoryItems.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.InventoryItems.PageInfo.EndCursor)
		}
	}
	return items, nil
}

// DeleteInventoryItem Delete inventory item.
func (a *ApiClient) DeleteInventoryItem(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteInventoryItemMutation {
	  deleteInventoryItem(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*inventoryItemDeleteResponse](resp)
	return err
}

// CreateRepo Create repo.
func (a *ApiClient) CreateRepo(ctx context.Context, parentID uuid.UUID, virtualRepo bool, repoType, name string, dataTypeID uuid.UUID, locationID uuid.UUID, data string) (*Repository, error) {
	// handle empty parent
	parent := fmt.Sprintf("\"%s\"", parentID)
	if (parentID == uuid.UUID{}) {
		parent = "null"
	}

	dataTypeIDStr := fmt.Sprintf("\"%s\"", dataTypeID)
	if (dataTypeID == uuid.UUID{}) {
		dataTypeIDStr = "null"
	}

	locationIDStr := fmt.Sprintf("\"%s\"", locationID)
	if (locationID == uuid.UUID{}) {
		locationIDStr = "null"
	}

	mutationStr := `mutation {
		createInventoryRepository(input: {
			parentID: %s,
			virtualRepo: %t,
			type: %s,
			name: "%s",
			dataTypeID: %s,
			locationID: %s,
			data: %s
		}) {
			inventoryRepository {
				id
				name
			}
		}
	}`
	mutation := fmt.Sprintf(mutationStr, parent, virtualRepo, repoType, name, dataTypeIDStr, locationIDStr, data)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}

	respData, err := ReadResponseData[*repositoryCreateResponse](resp)
	if err != nil {
		return nil, err
	}

	if (respData.Data.CreateInventoryRepository.InventoryRepository.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid inventory repository ID returned, %s", respData.Data.CreateInventoryRepository.InventoryRepository.ID)
	}
	return &respData.Data.CreateInventoryRepository.InventoryRepository, nil
}

// UpdateRepository Update repository.
func (a *ApiClient) UpdateRepository(ctx context.Context, ID uuid.UUID, parentID uuid.UUID, repoType, name string, dataTypeID uuid.UUID, locationID uuid.UUID, data string) (*Repository, error) {
	// handle empty parent
	parent := fmt.Sprintf("\"%s\"", parentID)
	if (parentID == uuid.UUID{}) {
		parent = "null"
	}

	dataTypeIDStr := fmt.Sprintf("\"%s\"", dataTypeID)
	if (dataTypeID == uuid.UUID{}) {
		dataTypeIDStr = "null"
	}

	locationIDStr := fmt.Sprintf("\"%s\"", locationID)
	if (locationID == uuid.UUID{}) {
		locationIDStr = "null"
	}

	mutationStr := `mutation {
		updateInventoryRepository(
			id: "%s",
			input: {
				parentID: %s,
				type: %s,
				name: "%s",
				dataTypeID: %s,
				locationID: %s,
				data: %s
			}) {
			inventoryRepository {
				id
				name
			}
		}
	}`
	mutation := fmt.Sprintf(mutationStr, ID, parent, repoType, name, dataTypeIDStr, locationIDStr, data)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}

	respData, err := ReadResponseData[*repositoryUpdateResponse](resp)
	if err != nil {
		return nil, err
	}

	if (respData.Data.UpdateInventoryRepository.InventoryRepository.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid inventory repository ID returned, %s", respData.Data.UpdateInventoryRepository.InventoryRepository.ID)
	}
	return &respData.Data.UpdateInventoryRepository.InventoryRepository, nil
}

// GetAllRepositories Get all repositories.
func (a *ApiClient) GetAllRepositories(ctx context.Context) (map[uuid.UUID]Repository, error) {
	mutationStr := `query DataType {
	  repositories(
		first: 100,
		after: %s
	) {
		totalCount
		edges {
		  node {
			id
			parentID
			name
			virtualRepo
			type
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	result := make(map[uuid.UUID]Repository)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*repositoriesQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.Repositories.Edges {
			result[edge.Node.ID] = edge.Node
		}
		// handle paging
		nextPage = data.Data.Repositories.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Repositories.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// GetRepositoriesWithFilter Get repositories with filter.
func (a *ApiClient) GetRepositoriesWithFilter(ctx context.Context, whereFilter string) ([]Repository, error) {
	mutationStr := `query QueryRepositoriesWithFilter {
	repositories(first:100,
	      after: %s,
		  orderBy: { direction: ASC, field: CREATED_AT },
		  where: %s
		) {
		  totalCount
		  edges {
			node {
			  	id
				tenantID
				dataTypeID
				name
				type
				parentID
				data
				createdAt
			}
		 }
		 pageInfo {
		   hasPreviousPage
		   startCursor
		   endCursor
		 }
	  }
	}`

	var repos []Repository
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor, whereFilter)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*repositoriesQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		if len(data.Errors) > 0 {
			return nil, errors.New(data.Errors[0].Message)
		}

		for _, edge := range data.Data.Repositories.Edges {
			repos = append(repos, edge.Node)
		}

		nextPage = data.Data.Repositories.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Repositories.PageInfo.EndCursor)
		}
	}
	return repos, nil
}

// SearchAvailableRepositories Search available repositories.
func (a *ApiClient) SearchAvailableRepositories(ctx context.Context, ID uuid.UUID) ([]Stock, error) {
	mutationStr := `query QueryStocksWithFilter {
	stocks(
		  where: {
					time: null,
					itemIDIn:["%s"],
        			quantityGT: 0
				}
		) {
		  totalCount
		  edges {
			node {
				repositoryID
				quantity
			}
		 }
		 pageInfo {
		   hasPreviousPage
		   startCursor
		   endCursor
		 }
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*stocksQueryResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		return nil, errors.New(data.Errors[0].Message)
	}

	var response []Stock
	for _, edge := range data.Data.Stocks.Edges {
		response = append(response, edge.Node)
	}

	return response, nil
}

// DeleteRepository Delete repository.
func (a *ApiClient) DeleteRepository(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteInventoryRepositoryMutation {
	  deleteInventoryRepository(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*deleteRepositoryResponse](resp)

	return err
}

// DeleteChildRepositories Delete child repositories.
func (a *ApiClient) DeleteChildRepositories(ctx context.Context, ID uuid.UUID, repoMap map[uuid.UUID]Repository) error {
	if len(repoMap) == 0 {
		return nil
	}

	for _, v := range repoMap {
		if v.ParentID == ID {
			err := a.DeleteChildRepositories(ctx, v.ID, repoMap)
			if err != nil {
				return err
			}
		}
	}

	delete(repoMap, ID)
	return a.DeleteRepository(ctx, ID)
}

// GetAllItems Get all items.
func (a *ApiClient) GetAllItems(ctx context.Context) ([]InventoryItem, error) {
	mutationStr := `query {
	  inventoryItems(
		first:100,
		after: %s,
	  ) {
		  totalCount
		  edges {
			  node {
				  id
			      sku
		     }
	     }
	     pageInfo {
			  hasNextPage
			  hasPreviousPage
			  startCursor
			  endCursor
        }
	  }
	}`

	items := []InventoryItem{}
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryItemQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to resultt
		for _, edge := range data.Data.InventoryItems.Edges {
			items = append(items, edge.Node)
		}
		// handle paging
		nextPage = data.Data.InventoryItems.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.InventoryItems.PageInfo.EndCursor)
		}
	}
	return items, nil
}

// CreateItemMovement Create item movement.
func (a *ApiClient) CreateItemMovement(ctx context.Context, itemID, sourceRepoID, targetRepoID uuid.UUID, quantity int) (*ItemMovement, error) {
	mutationStr := `mutation {
		createInventoryItemMovement(input: {
			itemID:"%s",
			fromID:"%s",
			toID:"%s",
			quantity: %d,
			handler: "Handler",
			executed: false
		}) {
			inventoryItemMovement{
				id
				executed
			}
		}}`
	mutation := fmt.Sprintf(mutationStr, itemID, sourceRepoID, targetRepoID, quantity)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}
	data, err := ReadResponseData[*createInventoryItemMovementResponse](resp)
	if err != nil {
		return nil, err
	}

	if (data.Data.CreateInventoryItemMovement.InventoryItemMovement.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid inventory item movement ID returned, %s", data.Data.CreateInventoryItemMovement.InventoryItemMovement.ID)
	}

	return &data.Data.CreateInventoryItemMovement.InventoryItemMovement, nil
}

// GetNotExecutedItemMovements Get not executed item movements.
func (a *ApiClient) GetNotExecutedItemMovements(ctx context.Context, handler string) ([]ItemMovement, error) {
	queryStr := `query {
	  itemMovements(
		first:100,
		after: %s,
		orderBy: { field: CREATED_AT, direction: ASC }
		where: {
			executed: false 
			handler: "%s"
	}) {
		totalCount
		edges {
		  node {
			id
			toID
			fromID
			orderID
			handler
			executed
			createdAt
			position
			collectionID
		  }
		  cursor
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}
`

	result := make([]ItemMovement, 0)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		query := fmt.Sprintf(queryStr, nextCursor, handler)
		resp, err := a.DoRequest(ctx, query)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryItemMovementsQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.ItemMovements.Edges {
			result = append(result, edge.Node)
		}

		// handle paging
		nextPage = data.Data.ItemMovements.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.ItemMovements.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// GetAllItemMovements Get all item movements.
func (a *ApiClient) GetAllItemMovements(ctx context.Context) ([]ItemMovement, error) {
	queryStr := `query {
	  itemMovements(
		first:100,
		after: %s,
		orderBy: { field: CREATED_AT, direction: ASC }) {
		totalCount
		edges {
		  node {
			id
			toID
			fromID
			orderID
			handler
			executed
			createdAt
			position
			collectionID
		  }
		  cursor
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}
`

	result := make([]ItemMovement, 0)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		query := fmt.Sprintf(queryStr, nextCursor)
		resp, err := a.DoRequest(ctx, query)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryItemMovementsQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.ItemMovements.Edges {
			result = append(result, edge.Node)
		}

		// handle paging
		nextPage = data.Data.ItemMovements.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.ItemMovements.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// DeleteItemMovement Delete item movement.
func (a *ApiClient) DeleteItemMovement(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteInventoryItemMovementMutation {
	  deleteInventoryItemMovement(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*deleteItemMovementResponse](resp)
	return err
}

// ExecuteItemMovement Execute item movement.
func (a *ApiClient) ExecuteItemMovement(ctx context.Context, movementID uuid.UUID) (*ItemMovement, error) {
	mutationStr := `mutation {
		executeInventoryItemMovement(
			id: "%s"
		) {
			inventoryItemMovement{
				id
			}
		}}`

	mutation := fmt.Sprintf(mutationStr, movementID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}
	data, err := ReadResponseData[*executeInventoryItemMovementResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		return nil, fmt.Errorf("%+v", data.Errors)
	}

	if (data.Data.ExecuteInventoryItemMovement.InventoryItemMovement.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid executed inventory item movement ID returned, %s", data.Data.ExecuteInventoryItemMovement.InventoryItemMovement.ID)
	}
	return &data.Data.ExecuteInventoryItemMovement.InventoryItemMovement, nil
}

// CreateOrder Create order.
func (a *ApiClient) CreateOrder(ctx context.Context, customerID, dataTypeID uuid.UUID, items []*OrderItem) (*Order, error) {
	mutationStr := `mutation {
		createPickingOrder(input: {
			customerID:"%s",
		    dataTypeID:"%s",
		    data: {
			  name: "%s"
		    },
			orderItems: [{
				sku: "%s",
				quantity: %d
			},{
				sku: "%s",
				quantity: %d
			}]
		}) {
			id
			customerID
			data
			orderitems{
				id
				sku
				quantity
			}
		}}`
	mutation := fmt.Sprintf(mutationStr, customerID, dataTypeID, gofakeit.Isin(),
		items[0].SKU, items[0].Quantity, items[1].SKU, items[1].Quantity)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}
	data, err := ReadResponseData[*createPickingOrderResponse](resp)
	if err != nil {
		return nil, err
	}

	if (data.Data.CreatePickingOrder.PickingOrder.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid picking order ID returned, %s", data.Data.CreatePickingOrder.PickingOrder.ID)
	}
	return &data.Data.CreatePickingOrder.PickingOrder, nil
}

// CreateOrderItem Create order item.
func (a *ApiClient) CreateOrderItem(ctx context.Context, orderID uuid.UUID, itemSKU string, quantity int) (*OrderItem, error) {
	mutationStr := `mutation {
		createPickingOrderItem(input: {
			sku:"%s",
			orderID:"%s",
			quantity: %d
		}) {
			id
		}}`
	mutation := fmt.Sprintf(mutationStr, itemSKU, orderID, quantity)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}
	data, err := ReadResponseData[*createPickingOrderItemResponse](resp)
	if err != nil {
		return nil, err
	}

	if (data.Data.CreatePickingOrderItem.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid picking order item ID returned, %s", data.Data.CreatePickingOrderItem.ID)
	}
	return &data.Data.CreatePickingOrderItem, nil
}

// GetAllPickingOrderItems Get all picking order items.
func (a *ApiClient) GetAllPickingOrderItems(ctx context.Context) ([]OrderItem, error) {
	mutationStr := `query {
	pickingOrderItems(first:100,
	      after: %s,
		  orderBy: { direction: ASC, field: CREATED_AT }
		) {
		  totalCount
		  edges {
			node {
			  id
			  sku
			  quantity
			}
		 }
		 pageInfo {
		   hasPreviousPage
		   startCursor
		   endCursor
		 }
	  }
	}`

	items := []OrderItem{}
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*pickingOrderItemQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to resultt
		for _, edge := range data.Data.PickingOrderItems.Edges {
			items = append(items, edge.Node)
		}
		// handle paging
		nextPage = data.Data.PickingOrderItems.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.PickingOrderItems.PageInfo.EndCursor)
		}
	}
	return items, nil
}

// DeletePickingOrderItem Delete picking order item.
func (a *ApiClient) DeletePickingOrderItem(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteOrderItemMutation {
	  deletePickingOrderItem(
		id: "%s"
	  ) {
		deletedID
	  }
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*pickingOrderItemDeleteResponse](resp)
	return err
}

// GetAllPickingOrders Get all picking orders.
func (a *ApiClient) GetAllPickingOrders(ctx context.Context) ([]Order, error) {
	mutationStr := `query {
	pickingOrders(first:100,
	      after: %s,
		  orderBy: { direction: ASC, field: CREATED_AT },
		) {
		  totalCount
		  edges {
			node {
			  	id
				tenantID
				customerID        
				dataTypeID
				data  
			  	createdAt
			}
		 }
		 pageInfo {
		   hasPreviousPage
		   startCursor
		   endCursor
		 }
	  }
	}`

	items := []Order{}
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*pickingOrderQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to resultt
		for _, edge := range data.Data.PickingOrders.Edges {
			items = append(items, edge.Node)
		}
		// handle paging
		nextPage = data.Data.PickingOrders.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.PickingOrders.PageInfo.EndCursor)
		}
	}
	return items, nil
}

// DeletePickingOrder Delete picking order.
func (a *ApiClient) DeletePickingOrder(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteOrderMutation {
		deletePickingOrder(
			id: "%s"
	) {
		deletedID
	}
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*pickingOrderDeleteResponse](resp)
	return err
}

// DeleteInventoryStock Delete inventory stock.
func (a *ApiClient) DeleteInventoryStock(ctx context.Context, ID uuid.UUID) error {
	mutationStr := `mutation DeleteInventoryStock {
		deleteInventoryStock(input: {
		repositoryID : "%s"
	}) {
		deletedID
		}
	}`

	mutation := fmt.Sprintf(mutationStr, ID)
	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return err
	}

	_, err = ReadResponseData[*deleteInventoryStockResponse](resp)
	return err
}

// CreateReceivingInbound Create receiving inbound.
func (a *ApiClient) CreateReceivingInbound(ctx context.Context, inboundDataType, inboundItemDataType, mainItemSKU, item1SKU string, serialNumbers []string) (*ReceivingInbound, []Workflows, error) {
	mutationStr := `mutation CreateReceivingInbound {
	  createReceivingInbound(
		input: {
		  orderID: "order"
		  dataTypeID: "%s"
		  data: {
				BinLocation: "U02 GA 0250-01-001"
			  }
		  inboundItems: [
			{
			  sku: "%s"
			  quantity: %d
			  dataTypeID: "%s"
			  data: {
 				Location: "location",
				Customer: "customer",
				BinLocation: "U02 GA 0250-01-001",
				EAN: "795711879242",
				Batch: "CN",
				HU_Number: "00640187298100002239",
				UOM: "PCE",
				Serialnumbers: [%s]
			  }
			},
			{
			  sku: "%s"
			  quantity: %d
			  dataTypeID: "%s"
			  data: {
 				Location: "location",
				Customer: "customer",
				BinLocation: "U02 GA 0250-01-001",
				EAN: "795711879243",
				Batch: "CN",
				HU_Number: "00640187298100002239",
				UOM: "PCE",
			  }
			}
		  ]
		}
	  ) {
		 receivingInbound {
		  id
		  tenantID
		  dataTypeID
		  createdAt
		  createdBy
		  updatedAt
		  updatedBy
		}
		workflows {
		  name
		  workflowID
		  workflowRunID
		  nextActivity {
			name
			properties {
			  key
			  value
			}
		  }
		}
	  }
	}`

	mutation := fmt.Sprintf(mutationStr,
		inboundDataType,
		mainItemSKU,
		len(serialNumbers),
		inboundItemDataType,
		fmt.Sprintf("\"%s\"", strings.Join(serialNumbers, "\", \"")),
		item1SKU,
		len(serialNumbers),
		inboundItemDataType)

	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, nil, err
	}

	data, err := ReadResponseData[*createReceivingInboundResponse](resp)
	if err != nil {
		return nil, nil, err
	}

	if (data.Data.CreateReceivingInbound.ReceivingInbound.ID == uuid.UUID{}) {
		return nil, nil, fmt.Errorf("invalid receiving inbound ID returned, %s", data.Data.CreateReceivingInbound.ReceivingInbound.ID)
	}
	result := &data.Data.CreateReceivingInbound.ReceivingInbound

	return result, data.Data.CreateReceivingInbound.Workflows, nil
}

// CreatePickingOrder Create picking order.
func (a *ApiClient) CreatePickingOrder(ctx context.Context, itemSetType, orderDataType, orderItemDataType, mainItemSKU, item1SKU string, quantity int) (*Order, error) {
	mutationStr := `mutation CreatePickingOrder {
		  createPickingOrder(
			input: {
			  customerID: "%s"
			  dataTypeID: "%s"
			  data: {
				Item_to_be_built: "%s"
				SAP_Storage_Location: "2210"
				Ordertype: "DBL Nachfüll"
				OutboundDate: "20240430"
				Reference1: "Referenz (aktuell nicht genutzt)"
				Reference2: "Referenz (aktuell nicht genutzt)"
				Quantity: %d
			  }
			  orderItems: [
				{
				  dataTypeID: "%s"
				  data: {
					Batch: "AT"
					MainItem: "1"
					Remarks: "Neues Artikellabel anbringen"
					LineReference1: "Referenz (aktuell nicht genutzt)"
					LineReference2: "Referenz (aktuell nicht genutzt)"
				  }
				  sku: "%s"
				  quantity: 1
				},
				{
				  dataTypeID: "%s"
				  data: {
					Batch: "CN"
					MainItem: "0"
					Remarks: "Achtung! Artikel befindet sich in einer Umverpackung!"
					LineReference1: "Referenz (aktuell nicht genutzt)"
					LineReference2: "Referenz (aktuell nicht genutzt)"
				  }
				  sku: "%s"
				  quantity: 1
				}
			  ]
			}
		  ) {
			 pickingOrder {
			  id
			  tenantID
			  customerID
			  dataTypeID
			  createdAt
			  createdBy
			  updatedAt
			  updatedBy
			}
		  }
		}`

	mutation := fmt.Sprintf(mutationStr,
		uuid.New().String(),
		orderDataType,
		itemSetType,
		quantity,
		orderItemDataType,
		mainItemSKU,
		orderItemDataType,
		item1SKU)

	resp, err := a.DoRequest(ctx, mutation)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*createPickingOrderResponse](resp)
	if err != nil {
		return nil, err
	}

	if (data.Data.CreatePickingOrder.PickingOrder.ID == uuid.UUID{}) {
		return nil, fmt.Errorf("invalid receiving inbound ID returned, %s", data.Data.CreatePickingOrder.PickingOrder.ID)
	}
	result := &data.Data.CreatePickingOrder.PickingOrder

	return result, nil
}

// GetCollections Get collections.
func (a *ApiClient) GetCollections(ctx context.Context, whereFilter string) ([]Collection, error) {
	queryStr := `
		{
		  inventoryCollections(
			first: 100,
			after: %s,
			where: %s
			) {
			edges {
			  node {
				id
			  }
			}
		  }
		}`

	result := make([]Collection, 0)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		query := fmt.Sprintf(queryStr, nextCursor, whereFilter)
		resp, err := a.DoRequest(ctx, query)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryCollectionQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		if len(data.Errors) > 0 {
			return nil, errors.New(data.Errors[0].Message)
		}

		// add items to result
		for _, edge := range data.Data.InventoryCollections.Edges {
			result = append(result, edge.Node)
		}

		// handle paging
		nextPage = data.Data.InventoryCollections.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.InventoryCollections.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// AssignCollection Assign collection.
func (a *ApiClient) AssignCollection(ctx context.Context, collectionID, userID string) ([]Workflows, error) {
	queryStr := `mutation UpdateInventoryCollectionMovement {
		  updateInventoryCollectionMovement(id: "%s", input: { assignee: "%s" }) {
			inventoryCollection {
			  id
			  assignee
			  assignmentDate
			}
			workflows
			{
			  name
			  workflowID
			  workflowRunID
			  nextActivity{
				name
				properties{
					key
					value
			 	 }  
				}
			}
		  }
		}`

	query := fmt.Sprintf(queryStr, collectionID, userID)
	resp, err := a.DoRequest(ctx, query)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*inventoryCollectionAssignResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		return nil, errors.New(data.Errors[0].Message)
	}

	return data.Data.UpdateInventoryCollectionMovement.Workflows, nil
}

// GetAssignedWorkflows Get assigned workflows.
func (a *ApiClient) GetAssignedWorkflows(ctx context.Context) ([]Workflows, error) {
	mutationStr := `mutation CheckAssignedWorkflows {
		  checkAssignedWorkflows(input: { includeCompleted: false }) {
			status
			workflow {
			  name
			  workflowID
			  workflowRunID
			  nextActivity {
				name
				properties {
				  key
				  value
				}
			  }
			}
		  }
		}`

	var workflows []Workflows
	resp, err := a.DoRequest(ctx, mutationStr)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*checkAssignedWorkflowsResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		return nil, errors.New(data.Errors[0].Message)
	}

	for _, edge := range data.Data.CheckAssignedWorkflows {
		workflows = append(workflows, edge.Workflow)
	}

	return workflows, nil
}

// UserDataInput User data input.
func (a *ApiClient) UserDataInput(ctx context.Context, workflowID, workflowRunID, activityName, activityData string) (*Workflows, error) {
	queryStr := `mutation UserDataInput {
		  userDataInput(
			input: {
			  workflowID: "%s"
			  workflowRunID: "%s"
			  activityName: "%s"
			  data: "%s"
			}
		  ) {
			status
			workflow {
			  name
			  workflowID
			  workflowRunID
			  nextActivity {
				name
				properties {
				  key
				  value
				}
			  }
			}
		  }
		}`

	query := fmt.Sprintf(queryStr, workflowID, workflowRunID, activityName, activityData)
	resp, err := a.DoRequest(ctx, query)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*userDataInputResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Errors) > 0 {
		return nil, errors.New(data.Errors[0].Message)
	}

	return &data.Data.UserDataInput.Workflow, nil
}

// GetAllItemMovementsByCollection Get all item movements by collection.
func (a *ApiClient) GetAllItemMovementsByCollection(ctx context.Context, collectionID uuid.UUID) ([]ItemMovement, error) {
	queryStr := `
		query {
		  itemMovements(
			first: 100
			after: %s,
			where: { collectionIDIn: ["%s"] }
			orderBy: { field: CREATED_AT, direction: ASC }
		  ) {
			totalCount
			edges {
			  node {
				id
				item {
				  id
				  sku
				  data
				}
				orderID
				collectionID
				position
				toID
				fromID
				handler
				quantity
				executed
				executedAt
				dataTypeID
				data
				tenantID
				createdAt
				updatedAt
			  }
			  cursor
			}
			pageInfo {
			  hasNextPage
			  hasPreviousPage
			  startCursor
			  endCursor
			}
		  }
		}
`

	result := make([]ItemMovement, 0)
	nextCursor := "null"
	nextPage := true
	for nextPage {
		query := fmt.Sprintf(queryStr, nextCursor, collectionID)
		resp, err := a.DoRequest(ctx, query)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*inventoryItemMovementsQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		if len(data.Errors) > 0 {
			return nil, errors.New(data.Errors[0].Message)
		}

		// add items to result
		for _, edge := range data.Data.ItemMovements.Edges {
			result = append(result, edge.Node)
		}

		// handle paging
		nextPage = data.Data.ItemMovements.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.ItemMovements.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// GetUsers Get users.
func (a *ApiClient) GetUsers(ctx context.Context) ([]User, error) {
	mutationStr := `query {
	  users (
		first: 100
		after: %s
	) {
		totalCount
		edges {
		  node {
			id
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	result := []User{}
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*usersQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.Users.Edges {
			result = append(result, edge.Node)
		}
		// handle paging
		nextPage = data.Data.Users.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Users.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// GetTenants Get tenants.
func (a *ApiClient) GetTenants(ctx context.Context) ([]User, error) {
	mutationStr := `query {
	  tenants (
		first: 100
		after: %s
	) {
		totalCount
		edges {
		  node {
			id
		  }
		}
		pageInfo {
		  hasNextPage
		  hasPreviousPage
		  startCursor
		  endCursor
		}
	  }
	}`

	var result []User
	nextCursor := "null"
	nextPage := true
	for nextPage {
		mutation := fmt.Sprintf(mutationStr, nextCursor)
		resp, err := a.DoRequest(ctx, mutation)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*usersQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		// add items to result
		for _, edge := range data.Data.Users.Edges {
			result = append(result, edge.Node)
		}
		// handle paging
		nextPage = data.Data.Users.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Users.PageInfo.EndCursor)
		}
	}
	return result, nil
}

// GetTransactionsByTimeRange Get transactions by time range.
func (a *ApiClient) GetTransactionsByTimeRange(ctx context.Context, start, end time.Time) ([]Transaction, error) {
	queryStr := `
	query {
		transactions(
			first: 100
			after: %s
			where: {
				createdAtGTE: "%s",
				createdAtLT: "%s",
				  hasRepositoryWith: {
						  virtualRepo: false
						}
			}
			orderBy: { field: CREATED_AT, direction: ASC }
		) {
			totalCount
			edges {
				node {
					id
					itemID
					repositoryID
					quantity
					type
					createdAt
				}
				cursor
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}`

	var result []Transaction
	nextCursor := "null"
	nextPage := true

	for nextPage {
		query := fmt.Sprintf(queryStr, nextCursor, start.Format(time.RFC3339), end.Format(time.RFC3339))
		resp, err := a.DoRequest(ctx, query)
		if err != nil {
			return nil, err
		}

		data, err := ReadResponseData[*transactionsQueryResponse](resp)
		if err != nil {
			return nil, err
		}

		for _, edge := range data.Data.Transactions.Edges {
			result = append(result, edge.Node)
		}

		nextPage = data.Data.Transactions.PageInfo.HasNextPage
		if nextPage {
			nextCursor = fmt.Sprintf("\"%s\"", *data.Data.Transactions.PageInfo.EndCursor)
		}
	}

	return result, nil
}

// GetStockByItemAndRepository Get stock by item and repository.
func (a *ApiClient) GetStockByItemAndRepository(ctx context.Context, itemID, repositoryID uuid.UUID, at *time.Time) (*Stock, error) {

	queryTime := "null"
	if at != nil {
		queryTime = fmt.Sprintf("\"%s\"", at.Format(time.RFC3339))
	}

	query := fmt.Sprintf(`
	query {
		stocks(
			where: {
				itemID: "%s",
				repositoryID: "%s",
				time: %s
			}
		) {
			edges {
				node {
					id
					itemID
					repositoryID
					quantity
				}
			}
		}
	}`, itemID, repositoryID, queryTime)

	resp, err := a.DoRequest(ctx, query)
	if err != nil {
		return nil, err
	}

	data, err := ReadResponseData[*stocksQueryResponse](resp)
	if err != nil {
		return nil, err
	}

	if len(data.Data.Stocks.Edges) == 0 {
		return nil, fmt.Errorf("no stock found for time %s", at.Format(time.RFC3339))
	}

	return &data.Data.Stocks.Edges[0].Node, nil
}
