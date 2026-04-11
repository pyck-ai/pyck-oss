package fieldnames

// Database column name constants for use with ent.Mutation.Field() and schema definitions.
// These are separate from FieldName enum because they use snake_case naming.
// Used by both ent/mixin and events packages to avoid duplication.
const (
	// DBColumnCreatedAt is the database column name for created_at (HistoryMixin).
	DBColumnCreatedAt = "created_at"
	// DBColumnCreatedBy is the database column name for created_by (HistoryMixin).
	DBColumnCreatedBy = "created_by"
	// DBColumnUpdatedAt is the database column name for updated_at (HistoryMixin).
	DBColumnUpdatedAt = "updated_at"
	// DBColumnUpdatedBy is the database column name for updated_by (HistoryMixin).
	DBColumnUpdatedBy = "updated_by"
	// DBColumnDeletedAt is the database column name for deleted_at (HistoryMixin).
	DBColumnDeletedAt = "deleted_at"
	// DBColumnDeletedBy is the database column name for deleted_by (HistoryMixin).
	DBColumnDeletedBy = "deleted_by"
)
