package resolvers

import (
	"context"
	"fmt"
	"sort"

	"entgo.io/contrib/entgql"
	"github.com/google/uuid"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entitem "github.com/pyck-ai/pyck/backend/inventory/ent/gen/item"
	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entrepository "github.com/pyck-ai/pyck/backend/inventory/ent/gen/repository"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
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

func BuildPredicates(input *ent.StockWhereInput) []entpredicate.Stock {
	if input == nil {
		return nil
	}
	predicates := make([]entpredicate.Stock, 0, 8)

	// ID
	if input.ID != nil {
		predicates = append(predicates, entstock.IDEQ(*input.ID))
	}
	if input.IDNEQ != nil {
		predicates = append(predicates, entstock.IDNEQ(*input.IDNEQ))
	}
	if len(input.IDIn) > 0 {
		predicates = append(predicates, entstock.IDIn(input.IDIn...))
	}
	if len(input.IDNotIn) > 0 {
		predicates = append(predicates, entstock.IDNotIn(input.IDNotIn...))
	}

	// TenantID
	if input.TenantID != nil {
		predicates = append(predicates, entstock.TenantIDEQ(*input.TenantID))
	}
	if input.TenantIDNEQ != nil {
		predicates = append(predicates, entstock.TenantIDNEQ(*input.TenantIDNEQ))
	}
	if len(input.TenantIDIn) > 0 {
		predicates = append(predicates, entstock.TenantIDIn(input.TenantIDIn...))
	}
	if len(input.TenantIDNotIn) > 0 {
		predicates = append(predicates, entstock.TenantIDNotIn(input.TenantIDNotIn...))
	}
	if input.TenantIDGT != nil {
		predicates = append(predicates, entstock.TenantIDGT(*input.TenantIDGT))
	}
	if input.TenantIDGTE != nil {
		predicates = append(predicates, entstock.TenantIDGTE(*input.TenantIDGTE))
	}
	if input.TenantIDLT != nil {
		predicates = append(predicates, entstock.TenantIDLT(*input.TenantIDLT))
	}
	if input.TenantIDLTE != nil {
		predicates = append(predicates, entstock.TenantIDLTE(*input.TenantIDLTE))
	}

	// RepositoryID
	if input.RepositoryID != nil {
		predicates = append(predicates, entstock.RepositoryIDEQ(*input.RepositoryID))
	}
	if input.RepositoryIDNEQ != nil {
		predicates = append(predicates, entstock.RepositoryIDNEQ(*input.RepositoryIDNEQ))
	}
	if len(input.RepositoryIDIn) > 0 {
		predicates = append(predicates, entstock.RepositoryIDIn(input.RepositoryIDIn...))
	}
	if len(input.RepositoryIDNotIn) > 0 {
		predicates = append(predicates, entstock.RepositoryIDNotIn(input.RepositoryIDNotIn...))
	}

	// ItemID
	if input.ItemID != nil {
		predicates = append(predicates, entstock.ItemIDEQ(*input.ItemID))
	}
	if input.ItemIDNEQ != nil {
		predicates = append(predicates, entstock.ItemIDNEQ(*input.ItemIDNEQ))
	}
	if len(input.ItemIDIn) > 0 {
		predicates = append(predicates, entstock.ItemIDIn(input.ItemIDIn...))
	}
	if len(input.ItemIDNotIn) > 0 {
		predicates = append(predicates, entstock.ItemIDNotIn(input.ItemIDNotIn...))
	}

	// Quantity
	if input.Quantity != nil {
		predicates = append(predicates, entstock.QuantityEQ(*input.Quantity))
	}
	if input.QuantityNEQ != nil {
		predicates = append(predicates, entstock.QuantityNEQ(*input.QuantityNEQ))
	}
	if len(input.QuantityIn) > 0 {
		predicates = append(predicates, entstock.QuantityIn(input.QuantityIn...))
	}
	if len(input.QuantityNotIn) > 0 {
		predicates = append(predicates, entstock.QuantityNotIn(input.QuantityNotIn...))
	}
	if input.QuantityGT != nil {
		predicates = append(predicates, entstock.QuantityGT(*input.QuantityGT))
	}
	if input.QuantityGTE != nil {
		predicates = append(predicates, entstock.QuantityGTE(*input.QuantityGTE))
	}
	if input.QuantityLT != nil {
		predicates = append(predicates, entstock.QuantityLT(*input.QuantityLT))
	}
	if input.QuantityLTE != nil {
		predicates = append(predicates, entstock.QuantityLTE(*input.QuantityLTE))
	}

	// OwnQuantity
	if input.OwnQuantity != nil {
		predicates = append(predicates, entstock.OwnQuantityEQ(*input.OwnQuantity))
	}
	if input.OwnQuantityNEQ != nil {
		predicates = append(predicates, entstock.OwnQuantityNEQ(*input.OwnQuantityNEQ))
	}
	if len(input.OwnQuantityIn) > 0 {
		predicates = append(predicates, entstock.OwnQuantityIn(input.OwnQuantityIn...))
	}
	if len(input.OwnQuantityNotIn) > 0 {
		predicates = append(predicates, entstock.OwnQuantityNotIn(input.OwnQuantityNotIn...))
	}
	if input.OwnQuantityGT != nil {
		predicates = append(predicates, entstock.OwnQuantityGT(*input.OwnQuantityGT))
	}
	if input.OwnQuantityGTE != nil {
		predicates = append(predicates, entstock.OwnQuantityGTE(*input.OwnQuantityGTE))
	}
	if input.OwnQuantityLT != nil {
		predicates = append(predicates, entstock.OwnQuantityLT(*input.OwnQuantityLT))
	}
	if input.OwnQuantityLTE != nil {
		predicates = append(predicates, entstock.OwnQuantityLTE(*input.OwnQuantityLTE))
	}

	// OwnIncomingStock
	if input.OwnIncomingStock != nil {
		predicates = append(predicates, entstock.OwnIncomingStockEQ(*input.OwnIncomingStock))
	}
	if input.OwnIncomingStockGT != nil {
		predicates = append(predicates, entstock.OwnIncomingStockGT(*input.OwnIncomingStockGT))
	}

	// OwnOutgoingStock
	if input.OwnOutgoingStock != nil {
		predicates = append(predicates, entstock.OwnOutgoingStockEQ(*input.OwnOutgoingStock))
	}
	if input.OwnOutgoingStockGT != nil {
		predicates = append(predicates, entstock.OwnOutgoingStockGT(*input.OwnOutgoingStockGT))
	}

	// IncomingStock
	if input.IncomingStock != nil {
		predicates = append(predicates, entstock.IncomingStockEQ(*input.IncomingStock))
	}
	if input.IncomingStockGT != nil {
		predicates = append(predicates, entstock.IncomingStockGT(*input.IncomingStockGT))
	}

	// OutgoingStock
	if input.OutgoingStock != nil {
		predicates = append(predicates, entstock.OutgoingStockEQ(*input.OutgoingStock))
	}
	if input.OutgoingStockGT != nil {
		predicates = append(predicates, entstock.OutgoingStockGT(*input.OutgoingStockGT))
	}

	// CreatedAt
	if input.CreatedAt != nil {
		predicates = append(predicates, entstock.CreatedAtEQ(*input.CreatedAt))
	}
	if input.CreatedAtNEQ != nil {
		predicates = append(predicates, entstock.CreatedAtNEQ(*input.CreatedAtNEQ))
	}
	if len(input.CreatedAtIn) > 0 {
		predicates = append(predicates, entstock.CreatedAtIn(input.CreatedAtIn...))
	}
	if len(input.CreatedAtNotIn) > 0 {
		predicates = append(predicates, entstock.CreatedAtNotIn(input.CreatedAtNotIn...))
	}
	if input.CreatedAtGT != nil {
		predicates = append(predicates, entstock.CreatedAtGT(*input.CreatedAtGT))
	}
	if input.CreatedAtGTE != nil {
		predicates = append(predicates, entstock.CreatedAtGTE(*input.CreatedAtGTE))
	}
	if input.CreatedAtLT != nil {
		predicates = append(predicates, entstock.CreatedAtLT(*input.CreatedAtLT))
	}
	if input.CreatedAtLTE != nil {
		predicates = append(predicates, entstock.CreatedAtLTE(*input.CreatedAtLTE))
	}

	// MovementID
	if input.MovementID != nil {
		predicates = append(predicates, entstock.MovementIDEQ(*input.MovementID))
	}
	if input.MovementIDIsNil {
		predicates = append(predicates, entstock.MovementIDIsNil())
	}
	if input.MovementIDNotNil {
		predicates = append(predicates, entstock.MovementIDNotNil())
	}

	// CreatedBy
	if input.CreatedBy != nil {
		predicates = append(predicates, entstock.CreatedByEQ(*input.CreatedBy))
	}
	if input.CreatedByNEQ != nil {
		predicates = append(predicates, entstock.CreatedByNEQ(*input.CreatedByNEQ))
	}
	if len(input.CreatedByIn) > 0 {
		predicates = append(predicates, entstock.CreatedByIn(input.CreatedByIn...))
	}
	if len(input.CreatedByNotIn) > 0 {
		predicates = append(predicates, entstock.CreatedByNotIn(input.CreatedByNotIn...))
	}

	// UpdatedBy
	if input.UpdatedBy != nil {
		predicates = append(predicates, entstock.UpdatedByEQ(*input.UpdatedBy))
	}
	if input.UpdatedByNEQ != nil {
		predicates = append(predicates, entstock.UpdatedByNEQ(*input.UpdatedByNEQ))
	}
	if len(input.UpdatedByIn) > 0 {
		predicates = append(predicates, entstock.UpdatedByIn(input.UpdatedByIn...))
	}
	if len(input.UpdatedByNotIn) > 0 {
		predicates = append(predicates, entstock.UpdatedByNotIn(input.UpdatedByNotIn...))
	}
	if input.UpdatedByIsNil {
		predicates = append(predicates, entstock.UpdatedByIsNil())
	}
	if input.UpdatedByNotNil {
		predicates = append(predicates, entstock.UpdatedByNotNil())
	}

	// UpdatedAt
	if input.UpdatedAt != nil {
		predicates = append(predicates, entstock.UpdatedAtEQ(*input.UpdatedAt))
	}
	if input.UpdatedAtNEQ != nil {
		predicates = append(predicates, entstock.UpdatedAtNEQ(*input.UpdatedAtNEQ))
	}
	if len(input.UpdatedAtIn) > 0 {
		predicates = append(predicates, entstock.UpdatedAtIn(input.UpdatedAtIn...))
	}
	if len(input.UpdatedAtNotIn) > 0 {
		predicates = append(predicates, entstock.UpdatedAtNotIn(input.UpdatedAtNotIn...))
	}
	if input.UpdatedAtGT != nil {
		predicates = append(predicates, entstock.UpdatedAtGT(*input.UpdatedAtGT))
	}
	if input.UpdatedAtGTE != nil {
		predicates = append(predicates, entstock.UpdatedAtGTE(*input.UpdatedAtGTE))
	}
	if input.UpdatedAtLT != nil {
		predicates = append(predicates, entstock.UpdatedAtLT(*input.UpdatedAtLT))
	}
	if input.UpdatedAtLTE != nil {
		predicates = append(predicates, entstock.UpdatedAtLTE(*input.UpdatedAtLTE))
	}

	// Edge: HasItem
	if input.HasItem != nil {
		if *input.HasItem {
			predicates = append(predicates, entstock.HasItem())
		} else {
			predicates = append(predicates, entstock.HasItemWith())
		}
	}
	for _, hw := range input.HasItemWith {
		predicates = append(predicates, entstock.HasItemWith(BuildInventoryItemPredicates(hw)...))
	}

	// Edge: HasRepository
	if input.HasRepository != nil {
		if *input.HasRepository {
			predicates = append(predicates, entstock.HasRepository())
		} else {
			predicates = append(predicates, entstock.HasRepositoryWith())
		}
	}
	for _, hw := range input.HasRepositoryWith {
		predicates = append(predicates, entstock.HasRepositoryWith(BuildRepositoryPredicates(hw)...))
	}

	// Logical ops
	if input.And != nil {
		for _, andInput := range input.And {
			if andInput != nil {
				subPreds := BuildPredicates(andInput)
				if len(subPreds) > 0 {
					predicates = append(predicates, entstock.And(subPreds...))
				}
			}
		}
	}
	if input.Or != nil {
		for _, orInput := range input.Or {
			if orInput != nil {
				subPreds := BuildPredicates(orInput)
				if len(subPreds) > 0 {
					predicates = append(predicates, entstock.Or(subPreds...))
				}
			}
		}
	}
	if input.Not != nil {
		subPreds := BuildPredicates(input.Not)
		if len(subPreds) > 0 {
			predicates = append(predicates, entstock.Not(entstock.And(subPreds...)))
		}
	}

	return predicates
}

func BuildInventoryItemPredicates(input *ent.InventoryItemWhereInput) []entpredicate.Item {
	if input == nil {
		return nil
	}

	var predicates []entpredicate.Item

	// ID
	if input.ID != nil {
		predicates = append(predicates, entitem.IDEQ(*input.ID))
	}
	if input.IDNEQ != nil {
		predicates = append(predicates, entitem.IDNEQ(*input.IDNEQ))
	}
	if len(input.IDIn) > 0 {
		predicates = append(predicates, entitem.IDIn(input.IDIn...))
	}
	if len(input.IDNotIn) > 0 {
		predicates = append(predicates, entitem.IDNotIn(input.IDNotIn...))
	}
	if input.IDGT != nil {
		predicates = append(predicates, entitem.IDGT(*input.IDGT))
	}
	if input.IDGTE != nil {
		predicates = append(predicates, entitem.IDGTE(*input.IDGTE))
	}
	if input.IDLT != nil {
		predicates = append(predicates, entitem.IDLT(*input.IDLT))
	}
	if input.IDLTE != nil {
		predicates = append(predicates, entitem.IDLTE(*input.IDLTE))
	}

	// TenantID
	if input.TenantID != nil {
		predicates = append(predicates, entitem.TenantIDEQ(*input.TenantID))
	}
	if input.TenantIDNEQ != nil {
		predicates = append(predicates, entitem.TenantIDNEQ(*input.TenantIDNEQ))
	}
	if len(input.TenantIDIn) > 0 {
		predicates = append(predicates, entitem.TenantIDIn(input.TenantIDIn...))
	}
	if len(input.TenantIDNotIn) > 0 {
		predicates = append(predicates, entitem.TenantIDNotIn(input.TenantIDNotIn...))
	}

	// DataTypeID
	if input.DataTypeID != nil {
		predicates = append(predicates, entitem.DataTypeIDEQ(*input.DataTypeID))
	}
	if input.DataTypeIDNEQ != nil {
		predicates = append(predicates, entitem.DataTypeIDNEQ(*input.DataTypeIDNEQ))
	}
	if len(input.DataTypeIDIn) > 0 {
		predicates = append(predicates, entitem.DataTypeIDIn(input.DataTypeIDIn...))
	}
	if len(input.DataTypeIDNotIn) > 0 {
		predicates = append(predicates, entitem.DataTypeIDNotIn(input.DataTypeIDNotIn...))
	}
	if input.DataTypeIDIsNil {
		predicates = append(predicates, entitem.DataTypeIDIsNil())
	}
	if input.DataTypeIDNotNil {
		predicates = append(predicates, entitem.DataTypeIDNotNil())
	}

	// DataTypeSlug
	if input.DataTypeSlug != nil {
		predicates = append(predicates, entitem.DataTypeSlugEQ(*input.DataTypeSlug))
	}
	if input.DataTypeSlugNEQ != nil {
		predicates = append(predicates, entitem.DataTypeSlugNEQ(*input.DataTypeSlugNEQ))
	}
	if len(input.DataTypeSlugIn) > 0 {
		predicates = append(predicates, entitem.DataTypeSlugIn(input.DataTypeSlugIn...))
	}
	if len(input.DataTypeSlugNotIn) > 0 {
		predicates = append(predicates, entitem.DataTypeSlugNotIn(input.DataTypeSlugNotIn...))
	}
	if input.DataTypeSlugContains != nil {
		predicates = append(predicates, entitem.DataTypeSlugContains(*input.DataTypeSlugContains))
	}
	if input.DataTypeSlugHasPrefix != nil {
		predicates = append(predicates, entitem.DataTypeSlugHasPrefix(*input.DataTypeSlugHasPrefix))
	}
	if input.DataTypeSlugHasSuffix != nil {
		predicates = append(predicates, entitem.DataTypeSlugHasSuffix(*input.DataTypeSlugHasSuffix))
	}
	if input.DataTypeSlugEqualFold != nil {
		predicates = append(predicates, entitem.DataTypeSlugEqualFold(*input.DataTypeSlugEqualFold))
	}
	if input.DataTypeSlugContainsFold != nil {
		predicates = append(predicates, entitem.DataTypeSlugContainsFold(*input.DataTypeSlugContainsFold))
	}
	if input.DataTypeSlugIsNil {
		predicates = append(predicates, entitem.DataTypeSlugIsNil())
	}
	if input.DataTypeSlugNotNil {
		predicates = append(predicates, entitem.DataTypeSlugNotNil())
	}

	// Sku
	if input.Sku != nil {
		predicates = append(predicates, entitem.SkuEQ(*input.Sku))
	}
	if input.SkuNEQ != nil {
		predicates = append(predicates, entitem.SkuNEQ(*input.SkuNEQ))
	}
	if len(input.SkuIn) > 0 {
		predicates = append(predicates, entitem.SkuIn(input.SkuIn...))
	}
	if len(input.SkuNotIn) > 0 {
		predicates = append(predicates, entitem.SkuNotIn(input.SkuNotIn...))
	}
	if input.SkuContains != nil {
		predicates = append(predicates, entitem.SkuContains(*input.SkuContains))
	}
	if input.SkuHasPrefix != nil {
		predicates = append(predicates, entitem.SkuHasPrefix(*input.SkuHasPrefix))
	}
	if input.SkuHasSuffix != nil {
		predicates = append(predicates, entitem.SkuHasSuffix(*input.SkuHasSuffix))
	}
	if input.SkuEqualFold != nil {
		predicates = append(predicates, entitem.SkuEqualFold(*input.SkuEqualFold))
	}
	if input.SkuContainsFold != nil {
		predicates = append(predicates, entitem.SkuContainsFold(*input.SkuContainsFold))
	}

	// CreatedBy
	if input.CreatedBy != nil {
		predicates = append(predicates, entitem.CreatedByEQ(*input.CreatedBy))
	}
	if input.CreatedByNEQ != nil {
		predicates = append(predicates, entitem.CreatedByNEQ(*input.CreatedByNEQ))
	}

	// UpdatedBy
	if input.UpdatedBy != nil {
		predicates = append(predicates, entitem.UpdatedByEQ(*input.UpdatedBy))
	}
	if input.UpdatedByNEQ != nil {
		predicates = append(predicates, entitem.UpdatedByNEQ(*input.UpdatedByNEQ))
	}
	if input.UpdatedByIsNil {
		predicates = append(predicates, entitem.UpdatedByIsNil())
	}
	if input.UpdatedByNotNil {
		predicates = append(predicates, entitem.UpdatedByNotNil())
	}

	// CreatedAt
	if input.CreatedAt != nil {
		predicates = append(predicates, entitem.CreatedAtEQ(*input.CreatedAt))
	}
	if input.CreatedAtGT != nil {
		predicates = append(predicates, entitem.CreatedAtGT(*input.CreatedAtGT))
	}
	if input.CreatedAtLT != nil {
		predicates = append(predicates, entitem.CreatedAtLT(*input.CreatedAtLT))
	}

	// UpdatedAt
	if input.UpdatedAt != nil {
		predicates = append(predicates, entitem.UpdatedAtEQ(*input.UpdatedAt))
	}
	if input.UpdatedAtGT != nil {
		predicates = append(predicates, entitem.UpdatedAtGT(*input.UpdatedAtGT))
	}
	if input.UpdatedAtLT != nil {
		predicates = append(predicates, entitem.UpdatedAtLT(*input.UpdatedAtLT))
	}

	// Recursive And/Or/Not
	if input.And != nil {
		for _, andInput := range input.And {
			if andInput != nil {
				sub := BuildInventoryItemPredicates(andInput)
				if len(sub) > 0 {
					predicates = append(predicates, entitem.And(sub...))
				}
			}
		}
	}
	if input.Or != nil {
		for _, orInput := range input.Or {
			if orInput != nil {
				sub := BuildInventoryItemPredicates(orInput)
				if len(sub) > 0 {
					predicates = append(predicates, entitem.Or(sub...))
				}
			}
		}
	}
	if input.Not != nil {
		sub := BuildInventoryItemPredicates(input.Not)
		if len(sub) > 0 {
			predicates = append(predicates, entitem.Not(entitem.And(sub...)))
		}
	}

	return predicates
}

func BuildRepositoryPredicates(input *ent.RepositoryWhereInput) []entpredicate.Repository {
	if input == nil {
		return nil
	}

	var predicates []entpredicate.Repository

	// ID
	if input.ID != nil {
		predicates = append(predicates, entrepository.IDEQ(*input.ID))
	}
	if input.IDNEQ != nil {
		predicates = append(predicates, entrepository.IDNEQ(*input.IDNEQ))
	}
	if len(input.IDIn) > 0 {
		predicates = append(predicates, entrepository.IDIn(input.IDIn...))
	}
	if len(input.IDNotIn) > 0 {
		predicates = append(predicates, entrepository.IDNotIn(input.IDNotIn...))
	}

	// TenantID
	if input.TenantID != nil {
		predicates = append(predicates, entrepository.TenantIDEQ(*input.TenantID))
	}
	if input.TenantIDNEQ != nil {
		predicates = append(predicates, entrepository.TenantIDNEQ(*input.TenantIDNEQ))
	}
	if len(input.TenantIDIn) > 0 {
		predicates = append(predicates, entrepository.TenantIDIn(input.TenantIDIn...))
	}
	if len(input.TenantIDNotIn) > 0 {
		predicates = append(predicates, entrepository.TenantIDNotIn(input.TenantIDNotIn...))
	}

	// DataTypeID
	if input.DataTypeID != nil {
		predicates = append(predicates, entrepository.DataTypeIDEQ(*input.DataTypeID))
	}
	if input.DataTypeIDNEQ != nil {
		predicates = append(predicates, entrepository.DataTypeIDNEQ(*input.DataTypeIDNEQ))
	}
	if len(input.DataTypeIDIn) > 0 {
		predicates = append(predicates, entrepository.DataTypeIDIn(input.DataTypeIDIn...))
	}
	if len(input.DataTypeIDNotIn) > 0 {
		predicates = append(predicates, entrepository.DataTypeIDNotIn(input.DataTypeIDNotIn...))
	}
	if input.DataTypeIDIsNil {
		predicates = append(predicates, entrepository.DataTypeIDIsNil())
	}
	if input.DataTypeIDNotNil {
		predicates = append(predicates, entrepository.DataTypeIDNotNil())
	}

	// Name
	if input.Name != nil {
		predicates = append(predicates, entrepository.NameEQ(*input.Name))
	}
	if input.NameNEQ != nil {
		predicates = append(predicates, entrepository.NameNEQ(*input.NameNEQ))
	}
	if len(input.NameIn) > 0 {
		predicates = append(predicates, entrepository.NameIn(input.NameIn...))
	}
	if len(input.NameNotIn) > 0 {
		predicates = append(predicates, entrepository.NameNotIn(input.NameNotIn...))
	}
	if input.NameContains != nil {
		predicates = append(predicates, entrepository.NameContains(*input.NameContains))
	}
	if input.NameHasPrefix != nil {
		predicates = append(predicates, entrepository.NameHasPrefix(*input.NameHasPrefix))
	}
	if input.NameHasSuffix != nil {
		predicates = append(predicates, entrepository.NameHasSuffix(*input.NameHasSuffix))
	}
	if input.NameEqualFold != nil {
		predicates = append(predicates, entrepository.NameEqualFold(*input.NameEqualFold))
	}
	if input.NameContainsFold != nil {
		predicates = append(predicates, entrepository.NameContainsFold(*input.NameContainsFold))
	}

	// Layout
	if input.Layout != nil {
		predicates = append(predicates, entrepository.LayoutEQ(*input.Layout))
	}
	if input.LayoutNEQ != nil {
		predicates = append(predicates, entrepository.LayoutNEQ(*input.LayoutNEQ))
	}
	if len(input.LayoutIn) > 0 {
		predicates = append(predicates, entrepository.LayoutIn(input.LayoutIn...))
	}
	if len(input.LayoutNotIn) > 0 {
		predicates = append(predicates, entrepository.LayoutNotIn(input.LayoutNotIn...))
	}
	if input.LayoutContains != nil {
		predicates = append(predicates, entrepository.LayoutContains(*input.LayoutContains))
	}
	if input.LayoutHasPrefix != nil {
		predicates = append(predicates, entrepository.LayoutHasPrefix(*input.LayoutHasPrefix))
	}
	if input.LayoutHasSuffix != nil {
		predicates = append(predicates, entrepository.LayoutHasSuffix(*input.LayoutHasSuffix))
	}
	if input.LayoutEqualFold != nil {
		predicates = append(predicates, entrepository.LayoutEqualFold(*input.LayoutEqualFold))
	}
	if input.LayoutContainsFold != nil {
		predicates = append(predicates, entrepository.LayoutContainsFold(*input.LayoutContainsFold))
	}
	if input.LayoutIsNil {
		predicates = append(predicates, entrepository.LayoutIsNil())
	}
	if input.LayoutNotNil {
		predicates = append(predicates, entrepository.LayoutNotNil())
	}

	// Type
	if input.Type != nil {
		predicates = append(predicates, entrepository.TypeEQ(*input.Type))
	}
	if input.TypeNEQ != nil {
		predicates = append(predicates, entrepository.TypeNEQ(*input.TypeNEQ))
	}
	if len(input.TypeIn) > 0 {
		predicates = append(predicates, entrepository.TypeIn(input.TypeIn...))
	}
	if len(input.TypeNotIn) > 0 {
		predicates = append(predicates, entrepository.TypeNotIn(input.TypeNotIn...))
	}

	// VirtualRepo
	if input.VirtualRepo != nil {
		predicates = append(predicates, entrepository.VirtualRepoEQ(*input.VirtualRepo))
	}
	if input.VirtualRepoNEQ != nil {
		predicates = append(predicates, entrepository.VirtualRepoNEQ(*input.VirtualRepoNEQ))
	}

	// Recursive And/Or/Not
	if input.And != nil {
		for _, andInput := range input.And {
			if andInput != nil {
				sub := BuildRepositoryPredicates(andInput)
				if len(sub) > 0 {
					predicates = append(predicates, entrepository.And(sub...))
				}
			}
		}
	}
	if input.Or != nil {
		for _, orInput := range input.Or {
			if orInput != nil {
				sub := BuildRepositoryPredicates(orInput)
				if len(sub) > 0 {
					predicates = append(predicates, entrepository.Or(sub...))
				}
			}
		}
	}
	if input.Not != nil {
		sub := BuildRepositoryPredicates(input.Not)
		if len(sub) > 0 {
			predicates = append(predicates, entrepository.Not(entrepository.And(sub...)))
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
