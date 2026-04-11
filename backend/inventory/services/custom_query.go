package services

import (
	"context"
	"fmt"
	"sort"

	"entgo.io/contrib/entgql"
	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/item"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	"github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
	"github.com/pyck-ai/pyck/backend/inventory/model"
)

func ExecuteCountQuery(ctx context.Context, tx *ent.Tx, query string, args ...interface{}) (int, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute count: %w", err)
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, fmt.Errorf("failed to scan count query: %w", err)
		}
	}
	return count, nil
}

func BuildPredicates(input *ent.StockWhereInput) []predicate.Stock {
	var predicates []predicate.Stock

	if input == nil {
		return predicates
	}

	// ID
	if input.ID != nil {
		predicates = append(predicates, stock.IDEQ(*input.ID))
	}
	if input.IDNEQ != nil {
		predicates = append(predicates, stock.IDNEQ(*input.IDNEQ))
	}
	if len(input.IDIn) > 0 {
		predicates = append(predicates, stock.IDIn(input.IDIn...))
	}
	if len(input.IDNotIn) > 0 {
		predicates = append(predicates, stock.IDNotIn(input.IDNotIn...))
	}

	// TenantID
	if input.TenantID != nil {
		predicates = append(predicates, stock.TenantIDEQ(*input.TenantID))
	}
	if input.TenantIDNEQ != nil {
		predicates = append(predicates, stock.TenantIDNEQ(*input.TenantIDNEQ))
	}
	if len(input.TenantIDIn) > 0 {
		predicates = append(predicates, stock.TenantIDIn(input.TenantIDIn...))
	}
	if len(input.TenantIDNotIn) > 0 {
		predicates = append(predicates, stock.TenantIDNotIn(input.TenantIDNotIn...))
	}
	if input.TenantIDGT != nil {
		predicates = append(predicates, stock.TenantIDGT(*input.TenantIDGT))
	}
	if input.TenantIDGTE != nil {
		predicates = append(predicates, stock.TenantIDGTE(*input.TenantIDGTE))
	}
	if input.TenantIDLT != nil {
		predicates = append(predicates, stock.TenantIDLT(*input.TenantIDLT))
	}
	if input.TenantIDLTE != nil {
		predicates = append(predicates, stock.TenantIDLTE(*input.TenantIDLTE))
	}

	// RepositoryID
	if input.RepositoryID != nil {
		predicates = append(predicates, stock.RepositoryIDEQ(*input.RepositoryID))
	}
	if input.RepositoryIDNEQ != nil {
		predicates = append(predicates, stock.RepositoryIDNEQ(*input.RepositoryIDNEQ))
	}
	if len(input.RepositoryIDIn) > 0 {
		predicates = append(predicates, stock.RepositoryIDIn(input.RepositoryIDIn...))
	}
	if len(input.RepositoryIDNotIn) > 0 {
		predicates = append(predicates, stock.RepositoryIDNotIn(input.RepositoryIDNotIn...))
	}

	// ItemID
	if input.ItemID != nil {
		predicates = append(predicates, stock.ItemIDEQ(*input.ItemID))
	}
	if input.ItemIDNEQ != nil {
		predicates = append(predicates, stock.ItemIDNEQ(*input.ItemIDNEQ))
	}
	if len(input.ItemIDIn) > 0 {
		predicates = append(predicates, stock.ItemIDIn(input.ItemIDIn...))
	}
	if len(input.ItemIDNotIn) > 0 {
		predicates = append(predicates, stock.ItemIDNotIn(input.ItemIDNotIn...))
	}

	// Quantity
	if input.Quantity != nil {
		predicates = append(predicates, stock.QuantityEQ(*input.Quantity))
	}
	if input.QuantityNEQ != nil {
		predicates = append(predicates, stock.QuantityNEQ(*input.QuantityNEQ))
	}
	if len(input.QuantityIn) > 0 {
		predicates = append(predicates, stock.QuantityIn(input.QuantityIn...))
	}
	if len(input.QuantityNotIn) > 0 {
		predicates = append(predicates, stock.QuantityNotIn(input.QuantityNotIn...))
	}
	if input.QuantityGT != nil {
		predicates = append(predicates, stock.QuantityGT(*input.QuantityGT))
	}
	if input.QuantityGTE != nil {
		predicates = append(predicates, stock.QuantityGTE(*input.QuantityGTE))
	}
	if input.QuantityLT != nil {
		predicates = append(predicates, stock.QuantityLT(*input.QuantityLT))
	}
	if input.QuantityLTE != nil {
		predicates = append(predicates, stock.QuantityLTE(*input.QuantityLTE))
	}

	// OwnQuantity
	if input.OwnQuantity != nil {
		predicates = append(predicates, stock.OwnQuantityEQ(*input.OwnQuantity))
	}
	if input.OwnQuantityNEQ != nil {
		predicates = append(predicates, stock.OwnQuantityNEQ(*input.OwnQuantityNEQ))
	}
	if len(input.OwnQuantityIn) > 0 {
		predicates = append(predicates, stock.OwnQuantityIn(input.OwnQuantityIn...))
	}
	if len(input.OwnQuantityNotIn) > 0 {
		predicates = append(predicates, stock.OwnQuantityNotIn(input.OwnQuantityNotIn...))
	}
	if input.OwnQuantityGT != nil {
		predicates = append(predicates, stock.OwnQuantityGT(*input.OwnQuantityGT))
	}
	if input.OwnQuantityGTE != nil {
		predicates = append(predicates, stock.OwnQuantityGTE(*input.OwnQuantityGTE))
	}
	if input.OwnQuantityLT != nil {
		predicates = append(predicates, stock.OwnQuantityLT(*input.OwnQuantityLT))
	}
	if input.OwnQuantityLTE != nil {
		predicates = append(predicates, stock.OwnQuantityLTE(*input.OwnQuantityLTE))
	}

	// OwnIncomingStock
	if input.OwnIncomingStock != nil {
		predicates = append(predicates, stock.OwnIncomingStockEQ(*input.OwnIncomingStock))
	}
	if input.OwnIncomingStockGT != nil {
		predicates = append(predicates, stock.OwnIncomingStockGT(*input.OwnIncomingStockGT))
	}

	// OwnOutgoingStock
	if input.OwnOutgoingStock != nil {
		predicates = append(predicates, stock.OwnOutgoingStockEQ(*input.OwnOutgoingStock))
	}
	if input.OwnOutgoingStockGT != nil {
		predicates = append(predicates, stock.OwnOutgoingStockGT(*input.OwnOutgoingStockGT))
	}

	// IncomingStock
	if input.IncomingStock != nil {
		predicates = append(predicates, stock.IncomingStockEQ(*input.IncomingStock))
	}
	if input.IncomingStockGT != nil {
		predicates = append(predicates, stock.IncomingStockGT(*input.IncomingStockGT))
	}

	// OutgoingStock
	if input.OutgoingStock != nil {
		predicates = append(predicates, stock.OutgoingStockEQ(*input.OutgoingStock))
	}
	if input.OutgoingStockGT != nil {
		predicates = append(predicates, stock.OutgoingStockGT(*input.OutgoingStockGT))
	}

	// CreatedAt
	if input.CreatedAt != nil {
		predicates = append(predicates, stock.CreatedAtEQ(*input.CreatedAt))
	}
	if input.CreatedAtNEQ != nil {
		predicates = append(predicates, stock.CreatedAtNEQ(*input.CreatedAtNEQ))
	}
	if len(input.CreatedAtIn) > 0 {
		predicates = append(predicates, stock.CreatedAtIn(input.CreatedAtIn...))
	}
	if len(input.CreatedAtNotIn) > 0 {
		predicates = append(predicates, stock.CreatedAtNotIn(input.CreatedAtNotIn...))
	}
	if input.CreatedAtGT != nil {
		predicates = append(predicates, stock.CreatedAtGT(*input.CreatedAtGT))
	}
	if input.CreatedAtGTE != nil {
		predicates = append(predicates, stock.CreatedAtGTE(*input.CreatedAtGTE))
	}
	if input.CreatedAtLT != nil {
		predicates = append(predicates, stock.CreatedAtLT(*input.CreatedAtLT))
	}
	if input.CreatedAtLTE != nil {
		predicates = append(predicates, stock.CreatedAtLTE(*input.CreatedAtLTE))
	}

	// MovementID
	if input.MovementID != nil {
		predicates = append(predicates, stock.MovementIDEQ(*input.MovementID))
	}
	if input.MovementIDIsNil {
		predicates = append(predicates, stock.MovementIDIsNil())
	}
	if input.MovementIDNotNil {
		predicates = append(predicates, stock.MovementIDNotNil())
	}

	// CreatedBy
	if input.CreatedBy != nil {
		predicates = append(predicates, stock.CreatedByEQ(*input.CreatedBy))
	}
	if input.CreatedByNEQ != nil {
		predicates = append(predicates, stock.CreatedByNEQ(*input.CreatedByNEQ))
	}
	if len(input.CreatedByIn) > 0 {
		predicates = append(predicates, stock.CreatedByIn(input.CreatedByIn...))
	}
	if len(input.CreatedByNotIn) > 0 {
		predicates = append(predicates, stock.CreatedByNotIn(input.CreatedByNotIn...))
	}

	// UpdatedBy
	if input.UpdatedBy != nil {
		predicates = append(predicates, stock.UpdatedByEQ(*input.UpdatedBy))
	}
	if input.UpdatedByNEQ != nil {
		predicates = append(predicates, stock.UpdatedByNEQ(*input.UpdatedByNEQ))
	}
	if len(input.UpdatedByIn) > 0 {
		predicates = append(predicates, stock.UpdatedByIn(input.UpdatedByIn...))
	}
	if len(input.UpdatedByNotIn) > 0 {
		predicates = append(predicates, stock.UpdatedByNotIn(input.UpdatedByNotIn...))
	}
	if input.UpdatedByIsNil {
		predicates = append(predicates, stock.UpdatedByIsNil())
	}
	if input.UpdatedByNotNil {
		predicates = append(predicates, stock.UpdatedByNotNil())
	}

	// UpdatedAt
	if input.UpdatedAt != nil {
		predicates = append(predicates, stock.UpdatedAtEQ(*input.UpdatedAt))
	}
	if input.UpdatedAtNEQ != nil {
		predicates = append(predicates, stock.UpdatedAtNEQ(*input.UpdatedAtNEQ))
	}
	if len(input.UpdatedAtIn) > 0 {
		predicates = append(predicates, stock.UpdatedAtIn(input.UpdatedAtIn...))
	}
	if len(input.UpdatedAtNotIn) > 0 {
		predicates = append(predicates, stock.UpdatedAtNotIn(input.UpdatedAtNotIn...))
	}
	if input.UpdatedAtGT != nil {
		predicates = append(predicates, stock.UpdatedAtGT(*input.UpdatedAtGT))
	}
	if input.UpdatedAtGTE != nil {
		predicates = append(predicates, stock.UpdatedAtGTE(*input.UpdatedAtGTE))
	}
	if input.UpdatedAtLT != nil {
		predicates = append(predicates, stock.UpdatedAtLT(*input.UpdatedAtLT))
	}
	if input.UpdatedAtLTE != nil {
		predicates = append(predicates, stock.UpdatedAtLTE(*input.UpdatedAtLTE))
	}

	// Edge: HasItem
	if input.HasItem != nil {
		if *input.HasItem {
			predicates = append(predicates, stock.HasItem())
		} else {
			predicates = append(predicates, stock.HasItemWith())
		}
	}
	for _, hw := range input.HasItemWith {
		predicates = append(predicates, stock.HasItemWith(BuildInventoryItemPredicates(hw)...))
	}

	// Edge: HasRepository
	if input.HasRepository != nil {
		if *input.HasRepository {
			predicates = append(predicates, stock.HasRepository())
		} else {
			predicates = append(predicates, stock.HasRepositoryWith())
		}
	}
	for _, hw := range input.HasRepositoryWith {
		predicates = append(predicates, stock.HasRepositoryWith(BuildRepositoryPredicates(hw)...))
	}

	// Logical ops
	if input.And != nil {
		for _, andInput := range input.And {
			if andInput != nil {
				subPreds := BuildPredicates(andInput)
				if len(subPreds) > 0 {
					predicates = append(predicates, stock.And(subPreds...))
				}
			}
		}
	}
	if input.Or != nil {
		for _, orInput := range input.Or {
			if orInput != nil {
				subPreds := BuildPredicates(orInput)
				if len(subPreds) > 0 {
					predicates = append(predicates, stock.Or(subPreds...))
				}
			}
		}
	}
	if input.Not != nil {
		subPreds := BuildPredicates(input.Not)
		if len(subPreds) > 0 {
			predicates = append(predicates, stock.Not(stock.And(subPreds...)))
		}
	}

	return predicates
}

func BuildInventoryItemPredicates(input *ent.InventoryItemWhereInput) []predicate.Item {
	if input == nil {
		return nil
	}

	var predicates []predicate.Item

	// ID
	if input.ID != nil {
		predicates = append(predicates, item.IDEQ(*input.ID))
	}
	if input.IDNEQ != nil {
		predicates = append(predicates, item.IDNEQ(*input.IDNEQ))
	}
	if len(input.IDIn) > 0 {
		predicates = append(predicates, item.IDIn(input.IDIn...))
	}
	if len(input.IDNotIn) > 0 {
		predicates = append(predicates, item.IDNotIn(input.IDNotIn...))
	}
	if input.IDGT != nil {
		predicates = append(predicates, item.IDGT(*input.IDGT))
	}
	if input.IDGTE != nil {
		predicates = append(predicates, item.IDGTE(*input.IDGTE))
	}
	if input.IDLT != nil {
		predicates = append(predicates, item.IDLT(*input.IDLT))
	}
	if input.IDLTE != nil {
		predicates = append(predicates, item.IDLTE(*input.IDLTE))
	}

	// TenantID
	if input.TenantID != nil {
		predicates = append(predicates, item.TenantIDEQ(*input.TenantID))
	}
	if input.TenantIDNEQ != nil {
		predicates = append(predicates, item.TenantIDNEQ(*input.TenantIDNEQ))
	}
	if len(input.TenantIDIn) > 0 {
		predicates = append(predicates, item.TenantIDIn(input.TenantIDIn...))
	}
	if len(input.TenantIDNotIn) > 0 {
		predicates = append(predicates, item.TenantIDNotIn(input.TenantIDNotIn...))
	}

	// DataTypeID
	if input.DataTypeID != nil {
		predicates = append(predicates, item.DataTypeIDEQ(*input.DataTypeID))
	}
	if input.DataTypeIDNEQ != nil {
		predicates = append(predicates, item.DataTypeIDNEQ(*input.DataTypeIDNEQ))
	}
	if len(input.DataTypeIDIn) > 0 {
		predicates = append(predicates, item.DataTypeIDIn(input.DataTypeIDIn...))
	}
	if len(input.DataTypeIDNotIn) > 0 {
		predicates = append(predicates, item.DataTypeIDNotIn(input.DataTypeIDNotIn...))
	}
	if input.DataTypeIDIsNil {
		predicates = append(predicates, item.DataTypeIDIsNil())
	}
	if input.DataTypeIDNotNil {
		predicates = append(predicates, item.DataTypeIDNotNil())
	}

	// DataTypeSlug
	if input.DataTypeSlug != nil {
		predicates = append(predicates, item.DataTypeSlugEQ(*input.DataTypeSlug))
	}
	if input.DataTypeSlugNEQ != nil {
		predicates = append(predicates, item.DataTypeSlugNEQ(*input.DataTypeSlugNEQ))
	}
	if len(input.DataTypeSlugIn) > 0 {
		predicates = append(predicates, item.DataTypeSlugIn(input.DataTypeSlugIn...))
	}
	if len(input.DataTypeSlugNotIn) > 0 {
		predicates = append(predicates, item.DataTypeSlugNotIn(input.DataTypeSlugNotIn...))
	}
	if input.DataTypeSlugContains != nil {
		predicates = append(predicates, item.DataTypeSlugContains(*input.DataTypeSlugContains))
	}
	if input.DataTypeSlugHasPrefix != nil {
		predicates = append(predicates, item.DataTypeSlugHasPrefix(*input.DataTypeSlugHasPrefix))
	}
	if input.DataTypeSlugHasSuffix != nil {
		predicates = append(predicates, item.DataTypeSlugHasSuffix(*input.DataTypeSlugHasSuffix))
	}
	if input.DataTypeSlugEqualFold != nil {
		predicates = append(predicates, item.DataTypeSlugEqualFold(*input.DataTypeSlugEqualFold))
	}
	if input.DataTypeSlugContainsFold != nil {
		predicates = append(predicates, item.DataTypeSlugContainsFold(*input.DataTypeSlugContainsFold))
	}
	if input.DataTypeSlugIsNil {
		predicates = append(predicates, item.DataTypeSlugIsNil())
	}
	if input.DataTypeSlugNotNil {
		predicates = append(predicates, item.DataTypeSlugNotNil())
	}

	// Sku
	if input.Sku != nil {
		predicates = append(predicates, item.SkuEQ(*input.Sku))
	}
	if input.SkuNEQ != nil {
		predicates = append(predicates, item.SkuNEQ(*input.SkuNEQ))
	}
	if len(input.SkuIn) > 0 {
		predicates = append(predicates, item.SkuIn(input.SkuIn...))
	}
	if len(input.SkuNotIn) > 0 {
		predicates = append(predicates, item.SkuNotIn(input.SkuNotIn...))
	}
	if input.SkuContains != nil {
		predicates = append(predicates, item.SkuContains(*input.SkuContains))
	}
	if input.SkuHasPrefix != nil {
		predicates = append(predicates, item.SkuHasPrefix(*input.SkuHasPrefix))
	}
	if input.SkuHasSuffix != nil {
		predicates = append(predicates, item.SkuHasSuffix(*input.SkuHasSuffix))
	}
	if input.SkuEqualFold != nil {
		predicates = append(predicates, item.SkuEqualFold(*input.SkuEqualFold))
	}
	if input.SkuContainsFold != nil {
		predicates = append(predicates, item.SkuContainsFold(*input.SkuContainsFold))
	}

	// CreatedBy
	if input.CreatedBy != nil {
		predicates = append(predicates, item.CreatedByEQ(*input.CreatedBy))
	}
	if input.CreatedByNEQ != nil {
		predicates = append(predicates, item.CreatedByNEQ(*input.CreatedByNEQ))
	}

	// UpdatedBy
	if input.UpdatedBy != nil {
		predicates = append(predicates, item.UpdatedByEQ(*input.UpdatedBy))
	}
	if input.UpdatedByNEQ != nil {
		predicates = append(predicates, item.UpdatedByNEQ(*input.UpdatedByNEQ))
	}
	if input.UpdatedByIsNil {
		predicates = append(predicates, item.UpdatedByIsNil())
	}
	if input.UpdatedByNotNil {
		predicates = append(predicates, item.UpdatedByNotNil())
	}

	// CreatedAt
	if input.CreatedAt != nil {
		predicates = append(predicates, item.CreatedAtEQ(*input.CreatedAt))
	}
	if input.CreatedAtGT != nil {
		predicates = append(predicates, item.CreatedAtGT(*input.CreatedAtGT))
	}
	if input.CreatedAtLT != nil {
		predicates = append(predicates, item.CreatedAtLT(*input.CreatedAtLT))
	}

	// UpdatedAt
	if input.UpdatedAt != nil {
		predicates = append(predicates, item.UpdatedAtEQ(*input.UpdatedAt))
	}
	if input.UpdatedAtGT != nil {
		predicates = append(predicates, item.UpdatedAtGT(*input.UpdatedAtGT))
	}
	if input.UpdatedAtLT != nil {
		predicates = append(predicates, item.UpdatedAtLT(*input.UpdatedAtLT))
	}

	// Recursive And/Or/Not
	if input.And != nil {
		for _, andInput := range input.And {
			if andInput != nil {
				sub := BuildInventoryItemPredicates(andInput)
				if len(sub) > 0 {
					predicates = append(predicates, item.And(sub...))
				}
			}
		}
	}
	if input.Or != nil {
		for _, orInput := range input.Or {
			if orInput != nil {
				sub := BuildInventoryItemPredicates(orInput)
				if len(sub) > 0 {
					predicates = append(predicates, item.Or(sub...))
				}
			}
		}
	}
	if input.Not != nil {
		sub := BuildInventoryItemPredicates(input.Not)
		if len(sub) > 0 {
			predicates = append(predicates, item.Not(item.And(sub...)))
		}
	}

	return predicates
}

func BuildRepositoryPredicates(input *ent.RepositoryWhereInput) []predicate.Repository {
	if input == nil {
		return nil
	}

	var predicates []predicate.Repository

	// ID
	if input.ID != nil {
		predicates = append(predicates, repository.IDEQ(*input.ID))
	}
	if input.IDNEQ != nil {
		predicates = append(predicates, repository.IDNEQ(*input.IDNEQ))
	}
	if len(input.IDIn) > 0 {
		predicates = append(predicates, repository.IDIn(input.IDIn...))
	}
	if len(input.IDNotIn) > 0 {
		predicates = append(predicates, repository.IDNotIn(input.IDNotIn...))
	}

	// TenantID
	if input.TenantID != nil {
		predicates = append(predicates, repository.TenantIDEQ(*input.TenantID))
	}
	if input.TenantIDNEQ != nil {
		predicates = append(predicates, repository.TenantIDNEQ(*input.TenantIDNEQ))
	}
	if len(input.TenantIDIn) > 0 {
		predicates = append(predicates, repository.TenantIDIn(input.TenantIDIn...))
	}
	if len(input.TenantIDNotIn) > 0 {
		predicates = append(predicates, repository.TenantIDNotIn(input.TenantIDNotIn...))
	}

	// DataTypeID
	if input.DataTypeID != nil {
		predicates = append(predicates, repository.DataTypeIDEQ(*input.DataTypeID))
	}
	if input.DataTypeIDNEQ != nil {
		predicates = append(predicates, repository.DataTypeIDNEQ(*input.DataTypeIDNEQ))
	}
	if len(input.DataTypeIDIn) > 0 {
		predicates = append(predicates, repository.DataTypeIDIn(input.DataTypeIDIn...))
	}
	if len(input.DataTypeIDNotIn) > 0 {
		predicates = append(predicates, repository.DataTypeIDNotIn(input.DataTypeIDNotIn...))
	}
	if input.DataTypeIDIsNil {
		predicates = append(predicates, repository.DataTypeIDIsNil())
	}
	if input.DataTypeIDNotNil {
		predicates = append(predicates, repository.DataTypeIDNotNil())
	}

	// Name
	if input.Name != nil {
		predicates = append(predicates, repository.NameEQ(*input.Name))
	}
	if input.NameNEQ != nil {
		predicates = append(predicates, repository.NameNEQ(*input.NameNEQ))
	}
	if len(input.NameIn) > 0 {
		predicates = append(predicates, repository.NameIn(input.NameIn...))
	}
	if len(input.NameNotIn) > 0 {
		predicates = append(predicates, repository.NameNotIn(input.NameNotIn...))
	}
	if input.NameContains != nil {
		predicates = append(predicates, repository.NameContains(*input.NameContains))
	}
	if input.NameHasPrefix != nil {
		predicates = append(predicates, repository.NameHasPrefix(*input.NameHasPrefix))
	}
	if input.NameHasSuffix != nil {
		predicates = append(predicates, repository.NameHasSuffix(*input.NameHasSuffix))
	}
	if input.NameEqualFold != nil {
		predicates = append(predicates, repository.NameEqualFold(*input.NameEqualFold))
	}
	if input.NameContainsFold != nil {
		predicates = append(predicates, repository.NameContainsFold(*input.NameContainsFold))
	}

	// Layout
	if input.Layout != nil {
		predicates = append(predicates, repository.LayoutEQ(*input.Layout))
	}
	if input.LayoutNEQ != nil {
		predicates = append(predicates, repository.LayoutNEQ(*input.LayoutNEQ))
	}
	if len(input.LayoutIn) > 0 {
		predicates = append(predicates, repository.LayoutIn(input.LayoutIn...))
	}
	if len(input.LayoutNotIn) > 0 {
		predicates = append(predicates, repository.LayoutNotIn(input.LayoutNotIn...))
	}
	if input.LayoutContains != nil {
		predicates = append(predicates, repository.LayoutContains(*input.LayoutContains))
	}
	if input.LayoutHasPrefix != nil {
		predicates = append(predicates, repository.LayoutHasPrefix(*input.LayoutHasPrefix))
	}
	if input.LayoutHasSuffix != nil {
		predicates = append(predicates, repository.LayoutHasSuffix(*input.LayoutHasSuffix))
	}
	if input.LayoutEqualFold != nil {
		predicates = append(predicates, repository.LayoutEqualFold(*input.LayoutEqualFold))
	}
	if input.LayoutContainsFold != nil {
		predicates = append(predicates, repository.LayoutContainsFold(*input.LayoutContainsFold))
	}
	if input.LayoutIsNil {
		predicates = append(predicates, repository.LayoutIsNil())
	}
	if input.LayoutNotNil {
		predicates = append(predicates, repository.LayoutNotNil())
	}

	// Type
	if input.Type != nil {
		predicates = append(predicates, repository.TypeEQ(*input.Type))
	}
	if input.TypeNEQ != nil {
		predicates = append(predicates, repository.TypeNEQ(*input.TypeNEQ))
	}
	if len(input.TypeIn) > 0 {
		predicates = append(predicates, repository.TypeIn(input.TypeIn...))
	}
	if len(input.TypeNotIn) > 0 {
		predicates = append(predicates, repository.TypeNotIn(input.TypeNotIn...))
	}

	// VirtualRepo
	if input.VirtualRepo != nil {
		predicates = append(predicates, repository.VirtualRepoEQ(*input.VirtualRepo))
	}
	if input.VirtualRepoNEQ != nil {
		predicates = append(predicates, repository.VirtualRepoNEQ(*input.VirtualRepoNEQ))
	}

	// Recursive And/Or/Not
	if input.And != nil {
		for _, andInput := range input.And {
			if andInput != nil {
				sub := BuildRepositoryPredicates(andInput)
				if len(sub) > 0 {
					predicates = append(predicates, repository.And(sub...))
				}
			}
		}
	}
	if input.Or != nil {
		for _, orInput := range input.Or {
			if orInput != nil {
				sub := BuildRepositoryPredicates(orInput)
				if len(sub) > 0 {
					predicates = append(predicates, repository.Or(sub...))
				}
			}
		}
	}
	if input.Not != nil {
		sub := BuildRepositoryPredicates(input.Not)
		if len(sub) > 0 {
			predicates = append(predicates, repository.Not(repository.And(sub...)))
		}
	}

	return predicates
}

func FlattenTreeEdges(roots []*model.RepositoryTree) []*model.RepositoryTreeEdge {
	var edges []*model.RepositoryTreeEdge
	for _, root := range roots {
		flattenTreeNode(root, &edges)
	}
	return edges
}

func flattenTreeNode(node *model.RepositoryTree, edges *[]*model.RepositoryTreeEdge) {
	*edges = append(*edges, &model.RepositoryTreeEdge{
		Cursor: entgql.Cursor[uuid.UUID]{ID: node.RepositoryID},
		Node:   node,
	})

	for _, child := range node.Children {
		flattenTreeNode(child.Node, edges)
	}
}

func PruneTree(roots []*model.RepositoryTree, include map[uuid.UUID]struct{}) []*model.RepositoryTree {
	prunedMap := make(map[uuid.UUID]*model.RepositoryTree)
	var prunedRoots []*model.RepositoryTree

	for _, root := range roots {
		if pruned := pruneNode(root, include, prunedMap); pruned != nil {
			prunedRoots = append(prunedRoots, pruned)
		}
	}

	sort.SliceStable(prunedRoots, func(i, j int) bool {
		return prunedRoots[i].CreatedAt.After(prunedRoots[j].CreatedAt)
	})

	return prunedRoots
}

func pruneNode(node *model.RepositoryTree, include map[uuid.UUID]struct{}, prunedMap map[uuid.UUID]*model.RepositoryTree) *model.RepositoryTree {
	if _, ok := include[node.RepositoryID]; !ok {
		return nil
	}

	prunedNode := &model.RepositoryTree{
		RepositoryID: node.RepositoryID,
		ParentID:     node.ParentID,
		Stocks:       node.Stocks,
		CreatedAt:    node.CreatedAt,
		Children:     []*model.RepositoryTreeEdge{},
	}

	prunedMap[node.RepositoryID] = prunedNode

	for _, child := range node.Children {
		if prunedChild := pruneNode(child.Node, include, prunedMap); prunedChild != nil {
			prunedNode.Children = append(prunedNode.Children, &model.RepositoryTreeEdge{
				Cursor: child.Cursor,
				Node:   prunedChild,
			})
		}
	}

	return prunedNode
}
