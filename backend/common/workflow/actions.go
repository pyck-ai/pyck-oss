package workflow

// AvailableActions reports which queries and updates a workflow currently
// accepts. The returned content changes dynamically as the workflow progresses.
type AvailableActions struct {
	Queries []ActionDefinition `json:"queries"`
	Updates []ActionDefinition `json:"updates"`
}

// ActionDefinition describes a single registered query or update.
// Enabled indicates whether the action is currently callable.
type ActionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}
