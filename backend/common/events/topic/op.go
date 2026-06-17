package topic

// Op constants name the CRUD operations that appear as the final segment of
// every MutationEventTopic (and that drive the OpUpdate→OpDelete remap for
// soft-deletes in the events hook).
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpDelete = "delete"
)
