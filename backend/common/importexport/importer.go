package importexport

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Importer orchestrates the import of entities from files. It streams records
// one at a time, resolves $ref references, and dispatches each entity to the
// correct service via the registry.
type Importer struct {
	registry        *Registry
	resolver        *RefResolver
	dryRun          bool
	continueOnError bool
	output          io.Writer
}

// ImporterOption configures an [Importer].
type ImporterOption func(*Importer)

// WithDryRun enables dry-run mode: records are parsed and $refs resolved, but
// no mutations are executed.
func WithDryRun(dryRun bool) ImporterOption {
	return func(i *Importer) { i.dryRun = dryRun }
}

// WithContinueOnError allows the import to continue after a record fails
// instead of aborting immediately.
func WithContinueOnError(cont bool) ImporterOption {
	return func(i *Importer) { i.continueOnError = cont }
}

// WithOutput sets the writer for progress output. Defaults to os.Stdout.
func WithOutput(w io.Writer) ImporterOption {
	return func(i *Importer) { i.output = w }
}

// NewImporter creates an importer backed by the given registry.
func NewImporter(registry *Registry, opts ...ImporterOption) *Importer {
	imp := &Importer{
		registry: registry,
		resolver: NewRefResolver(registry),
		output:   os.Stdout,
	}
	for _, opt := range opts {
		opt(imp)
	}
	return imp
}

// ImportFiles streams and imports entities from the given file paths. Records
// are processed one at a time without buffering the entire file in memory.
func (imp *Importer) ImportFiles(ctx context.Context, paths []string) (*ImportResult, error) {
	result := &ImportResult{}

	for record, err := range StreamFiles(paths) {
		if err != nil {
			if !imp.continueOnError {
				return result, fmt.Errorf("parse: %w", err)
			}
			fmt.Fprintf(imp.output, "ERROR: parse: %s\n", err)
			// Parse errors embed source location in the error message itself,
			// so ImportError.Record is left as zero value intentionally.
			result.Errors = append(result.Errors, ImportError{Err: err})
			continue
		}

		if err := imp.processRecord(ctx, record, result); err != nil {
			importErr := ImportError{Record: record, Err: err}
			result.Errors = append(result.Errors, importErr)

			if !imp.continueOnError {
				return result, importErr
			}
			fmt.Fprintf(imp.output, "ERROR: %s\n", importErr)
		}
	}

	imp.printSummary(result)
	return result, nil
}

func (imp *Importer) processRecord(ctx context.Context, record ImportRecord, result *ImportResult) error {
	desc, ok := imp.registry.Get(record.TypeName)
	if !ok {
		return fmt.Errorf("%w %q (registered types: %v)",
			ErrUnknownEntityType, record.TypeName, imp.registry.TypeNames())
	}

	// Resolve $ref fields in-place.
	if err := imp.resolver.ResolveRefs(ctx, record.Data); err != nil {
		return fmt.Errorf("resolve refs: %w", err)
	}

	// Extract identity value for existence check.
	// For create-only entities (no IdentityField), fall back to "id" if present.
	identityVal, hasIdentity := record.Data[desc.IdentityField]
	if !hasIdentity && desc.IdentityField == "" {
		if idVal, ok := record.Data["id"]; ok {
			identityVal = idVal
			hasIdentity = true
		}
	}

	// Create-only entities (no Update func) are always created unless
	// an existing record with the same id is found (then skipped).
	createOnly := desc.Update == nil

	if imp.dryRun {
		action := "create"
		if hasIdentity && createOnly {
			action = "create/skip"
		} else if hasIdentity {
			action = "create/update"
		}
		fmt.Fprintf(imp.output, "[dry-run] %s %s (identity: %v)\n",
			action, record.TypeName, identityVal)
		return nil
	}

	// Query for existing entity by identity field (or by id for create-only).
	existingID, err := imp.findExisting(ctx, desc, identityVal, hasIdentity)
	if err != nil {
		return fmt.Errorf("query existing %s: %w", record.TypeName, err)
	}

	// In case the create-only entity already exists, skip silently.
	if existingID != "" && createOnly {
		result.Skipped++
		if err := imp.trackAlias(record, existingID); err != nil {
			return err
		}
		fmt.Fprintf(imp.output, "skipped %s (id=%s, already exists)\n",
			record.TypeName, existingID)
		return nil
	}

	if existingID != "" {
		return imp.updateEntity(ctx, desc, record, existingID, identityVal, result)
	}
	return imp.createEntity(ctx, desc, record, identityVal, result)
}

func (imp *Importer) updateEntity(ctx context.Context, desc *EntityDescriptor, record ImportRecord, existingID string, identityVal any, result *ImportResult) error {
	updated, err := desc.Update(ctx, existingID, record.Data)
	if err != nil {
		return fmt.Errorf("update %s (id=%s): %w", record.TypeName, existingID, err)
	}
	result.Updated++
	imp.resolver.Track(record.TypeName, updated, existingID)
	if err := imp.trackAlias(record, existingID); err != nil {
		return err
	}
	fmt.Fprintf(imp.output, "updated %s %v (id=%s)\n",
		record.TypeName, identityVal, existingID)
	return nil
}

func (imp *Importer) createEntity(ctx context.Context, desc *EntityDescriptor, record ImportRecord, identityVal any, result *ImportResult) error {
	created, err := desc.Create(ctx, record.Data)
	if err != nil {
		return fmt.Errorf("create %s: %w", record.TypeName, err)
	}
	id, ok := created["id"].(string)
	if !ok || id == "" {
		return fmt.Errorf("%w for %q", ErrMissingID, record.TypeName)
	}
	result.Created++
	imp.resolver.Track(record.TypeName, created, id)
	if err := imp.trackAlias(record, id); err != nil {
		return err
	}
	fmt.Fprintf(imp.output, "created %s %v (id=%s)\n",
		record.TypeName, identityVal, id)
	return nil
}

// trackAlias stores a $refid alias → ID mapping if the record has one.
func (imp *Importer) trackAlias(record ImportRecord, id string) error {
	if record.RefID == "" {
		return nil
	}
	return imp.resolver.TrackAlias(record.RefID, id)
}

// findExisting looks up an entity by its identity field value. For create-only
// entities (no identity field), it falls back to looking up by "id".
// Returns the existing entity's ID or "" if not found.
func (imp *Importer) findExisting(ctx context.Context, desc *EntityDescriptor, identityVal any, hasIdentity bool) (string, error) {
	if !hasIdentity {
		return "", nil
	}

	field := desc.IdentityField
	if field == "" {
		field = "id" // create-only entities: look up by id
	}

	first := 1
	existing, err := desc.List(ctx, nil, &first, map[string]any{field: identityVal})
	if err != nil {
		return "", err
	}

	if len(existing.Nodes) > 0 {
		if id, ok := existing.Nodes[0]["id"].(string); ok {
			return id, nil
		}
	}

	return "", nil
}

func (imp *Importer) printSummary(result *ImportResult) {
	fmt.Fprintf(imp.output, "\nImport complete: %d created, %d updated, %d skipped, %d errors\n",
		result.Created, result.Updated, result.Skipped, len(result.Errors))
}
