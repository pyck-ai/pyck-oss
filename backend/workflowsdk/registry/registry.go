package registry

type Registry struct {
	activities ActivityRegistry
	workflows  WorkflowRegistry
}

func (r *Registry) RegisterWorkflow(workflow any, opts ...WorkflowRegistryOption) (string, error) {
	return r.workflows.Register(workflow, opts...)
}

func (r *Registry) Workflows() []WorkflowRegistryEntry {
	return r.workflows.Items()
}

func (r *Registry) RegisterActivities(taskQueue string, activities any) error {
	return r.activities.Register(taskQueue, activities)
}

func (r *Registry) Activities() []ActivityRegistryEntry {
	return r.activities.Items()
}
