package apiclient

import (
	"time"

	"github.com/google/uuid"
)

type pageInfo struct {
	HasNextPage     bool
	HasPreviousPage bool
	StartCursor     *string
	EndCursor       *string
}

type ApiRequest struct {
	OperationName string `json:"operationName,omitempty"`
	Query         string `json:"query"`
}

type ApiError struct {
	Message string
	Path    []string
}

type DataType struct {
	ID          uuid.UUID
	JSONSchema  string
	Name        string
	Description string
	IsDefault   bool `json:"default"`
	Entity      string
}

type dataTypeCreateResponse struct {
	Errors []ApiError
	Data   struct {
		CreateDataType DataType
	}
}

type dataTypeUpdateResponse struct {
	Errors []ApiError
	Data   struct {
		UpdateDataType DataType
	}
}

type dataTypesQueryResponse struct {
	Errors []ApiError
	Data   struct {
		DataTypes struct {
			TotalCount int
			Edges      []struct {
				Node   DataType
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type dataTypeDeleteResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteDataType struct {
			DeletedID uuid.UUID
		}
	}
}

type Supplier struct {
	ID uuid.UUID
}

type supplierCreateResponse struct {
	Errors []ApiError
	Data   struct {
		CreateSupplier Supplier
	}
}

type Customer struct {
	ID   uuid.UUID
	Data map[string]interface{}
}

type customerCreateResponse struct {
	Errors []ApiError
	Data   struct {
		CreateCustomer Customer
	}
}

type customersQueryResponse struct {
	Errors []ApiError
	Data   struct {
		Customers struct {
			TotalCount int
			Edges      []struct {
				Node   Customer
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type deleteCustomerResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteCustomer struct {
			DeletedID uuid.UUID
		}
	}
}

type supplierQueryResponse struct {
	Errors []ApiError
	Data   struct {
		Suppliers struct {
			TotalCount int
			Edges      []struct {
				Node   Supplier
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type deleteSupplierResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteSupplier struct {
			DeletedID uuid.UUID
		}
	}
}

type InventoryItem struct {
	ID  uuid.UUID
	SKU string
}

type inventoryItemCreateResponse struct {
	Errors []ApiError
	Data   struct {
		CreateInventoryItem struct {
			InventoryItem InventoryItem
		}
	}
}

type inventoryItemQueryResponse struct {
	Errors []ApiError
	Data   struct {
		InventoryItems struct {
			TotalCount int
			Edges      []struct {
				Node   InventoryItem
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type inventoryItemDeleteResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteInventoryItem struct {
			DeletedID uuid.UUID
		}
	}
}

type Repository struct {
	ID          uuid.UUID
	ParentID    uuid.UUID
	Name        string
	Type        string
	VirtualRepo bool
	Data        map[string]interface{}
}

type repositoryCreateResponse struct {
	Errors []ApiError
	Data   struct {
		CreateInventoryRepository struct {
			InventoryRepository Repository
		}
	}
}

type repositoryUpdateResponse struct {
	Errors []ApiError
	Data   struct {
		UpdateInventoryRepository struct {
			InventoryRepository Repository
		}
	}
}

type repositoriesQueryResponse struct {
	Errors []ApiError
	Data   struct {
		Repositories struct {
			TotalCount int
			Edges      []struct {
				Node   Repository
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type Stock struct {
	ID           uuid.UUID
	ItemID       uuid.UUID
	RepositoryID uuid.UUID
	Quantity     int
}

type stocksQueryResponse struct {
	Errors []ApiError
	Data   struct {
		Stocks struct {
			TotalCount int
			Edges      []struct {
				Node   Stock
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type deleteRepositoryResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteInventoryRepository struct {
			DeletedID uuid.UUID
		}
	}
}

type ItemMovement struct {
	ID           uuid.UUID
	ToID         uuid.UUID
	FromID       uuid.UUID
	OrderID      uuid.UUID
	CollectionID uuid.UUID
	Handler      string
	Executed     bool
	Position     int
	CreatedAt    time.Time
}

type createInventoryItemMovementResponse struct {
	Errors []ApiError
	Data   struct {
		CreateInventoryItemMovement struct {
			InventoryItemMovement ItemMovement
		}
	}
}

type executeInventoryItemMovementResponse struct {
	Errors []ApiError
	Data   struct {
		ExecuteInventoryItemMovement struct {
			InventoryItemMovement ItemMovement
		}
	}
}

type inventoryItemMovementsQueryResponse struct {
	Errors []ApiError
	Data   struct {
		ItemMovements struct {
			TotalCount int
			Edges      []struct {
				Node   ItemMovement
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type deleteItemMovementResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteInventoryItemMovement struct {
			DeletedID uuid.UUID
		}
	}
}

type Order struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	CustomerID uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]interface{}
	OrderItems []OrderItem
	CreatedAt  time.Time
	CreatedBy  string
	UpdatedAt  time.Time
	UpdatedBy  string
}

type createPickingOrderResponse struct {
	Errors []ApiError
	Data   struct {
		CreatePickingOrder struct {
			PickingOrder Order
		}
	}
}

type pickingOrderQueryResponse struct {
	Errors []ApiError
	Data   struct {
		PickingOrders struct {
			TotalCount int
			Edges      []struct {
				Node   Order
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type pickingOrderDeleteResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeletePickingOrder struct {
			DeletedID uuid.UUID
		}
	}
}

type OrderItem struct {
	ID       uuid.UUID
	SKU      string
	Quantity int
}

type createPickingOrderItemResponse struct {
	Errors []ApiError
	Data   struct {
		CreatePickingOrderItem OrderItem
	}
}

type pickingOrderItemQueryResponse struct {
	Errors []ApiError
	Data   struct {
		PickingOrderItems struct {
			TotalCount int
			Edges      []struct {
				Node   OrderItem
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type pickingOrderItemDeleteResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeletePickingOrderItem struct {
			DeletedID uuid.UUID
		}
	}
}

type deleteInventoryStockResponse struct {
	Errors []struct {
		Message string
		Path    []string
	}
	Data struct {
		DeleteInventoryStock struct {
			DeletedID uuid.UUID
		}
	}
}

type ReceivingInbound struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	CustomerID uuid.UUID
	DataTypeID uuid.UUID
	Data       map[string]interface{}
	OrderItems []ReceivingInboundItem
	CreatedAt  time.Time
	CreatedBy  uuid.UUID
	UpdatedAt  time.Time
	UpdatedBy  uuid.UUID
}

type ReceivingInboundItem struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	InboundID  uuid.UUID
	DataTypeID uuid.UUID
	Sku        string
	Quantity   int64
	Data       map[string]interface{}
}

type createReceivingInboundResponse struct {
	Errors []ApiError
	Data   struct {
		CreateReceivingInbound struct {
			ReceivingInbound ReceivingInbound
			Workflows        []Workflows
		}
	}
}

type Collection struct {
	ID       uuid.UUID
	Assignee uuid.UUID
}

type inventoryCollectionQueryResponse struct {
	Errors []ApiError
	Data   struct {
		InventoryCollections struct {
			TotalCount int
			Edges      []struct {
				Node   Collection
				Cursor string
			}
			PageInfo pageInfo
		}
	}
}

type inventoryCollectionAssignResponse struct {
	Errors []ApiError
	Data   struct {
		UpdateInventoryCollectionMovement struct {
			Workflows []Workflows
		}
	}
}

type CollectionMovement struct {
	ID           uuid.UUID
	MovementType string
}

type CreateInventoryCollectionMovementResponse struct {
	ID        uuid.UUID
	Movements []CollectionMovement
}

type userDataInputResponse struct {
	Errors []ApiError
	Data   struct {
		UserDataInput struct {
			Workflow Workflows
		}
	}
}

type ItemSet struct {
	ID  uuid.UUID
	Sku string
}

type WorkflowsNextActivity struct {
	Name       string
	Properties []struct {
		Key   string
		Value string
	}
}

type Workflow struct {
	ID         uuid.UUID
	Name       string
	TenantID   uuid.UUID
	CreatedAt  time.Time
	CreatedBy  string
	UpdatedAt  time.Time
	UpdatedBy  string
	Data       map[string]interface{}
	DataTypeID uuid.UUID
}

type checkAssignedWorkflows struct {
	Status   string `json:"status"`
	Workflow Workflows
}

type Workflows struct {
	WorkflowID    string
	WorkflowRunID string
	Name          string
	NextActivity  WorkflowsNextActivity
}

type checkAssignedWorkflowsResponse struct {
	Errors []ApiError
	Data   struct {
		CheckAssignedWorkflows []checkAssignedWorkflows
	}
}

type User struct {
	ID uuid.UUID
}

type usersQueryResponse struct {
	Errors []ApiError
	Data   struct {
		Users struct {
			TotalCount int
			Edges      []struct {
				Node   User
				Cursor string
			}
			PageInfo struct {
				HasNextPage     bool
				HasPreviousPage bool
				StartCursor     *string
				EndCursor       *string
			}
		}
	}
}

type Transaction struct {
	ID           uuid.UUID
	ItemID       uuid.UUID
	RepositoryID uuid.UUID
	Quantity     int
	Type         string
	CreatedAt    time.Time
}

type transactionsQueryResponse struct {
	Errors []ApiError
	Data   struct {
		Transactions struct {
			Edges []struct {
				Node   Transaction
				Cursor string
			}
			PageInfo struct {
				HasNextPage bool
				EndCursor   *string
			}
		}
	}
}
