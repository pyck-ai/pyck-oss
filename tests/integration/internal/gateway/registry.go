package gateway

import (
	"fmt"
	nethttp "net/http"

	"github.com/gqlgo/gqlgenc/clientv2"

	"github.com/pyck-ai/pyck/backend/common/importexport"
	inventoryapi "github.com/pyck-ai/pyck/backend/inventory/api"
	maindataapi "github.com/pyck-ai/pyck/backend/main-data/api"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"
	pickingapi "github.com/pyck-ai/pyck/backend/picking/api"
	receivingapi "github.com/pyck-ai/pyck/backend/receiving/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
)

// NewImportExportRegistry builds an importexport.Registry with every
// service's entities registered against a client pointed at the gateway and
// authenticated with the given bearer token. Pass a tenant-scoped PAT so the
// whole round-trip (import + export) is confined to that tenant.
//
// This is the same wiring the import/export suite needs that production
// tooling does — registering all five federated subgraphs' entities on one
// registry so a single import pass can resolve cross-service $refid aliases.
func NewImportExportRegistry(cfg *config.Config, token string) (*importexport.Registry, error) {
	intercept := bearerInterceptor(token)
	opts := &clientv2.Options{ParseDataAlongWithErrors: true}
	reg := importexport.NewRegistry()

	registrars := []struct {
		name string
		fn   func() error
	}{
		{"management", func() error {
			return managementapi.RegisterEntities(reg, managementapi.NewClient(nethttp.DefaultClient, cfg.GatewayURL, opts, intercept))
		}},
		{"inventory", func() error {
			return inventoryapi.RegisterEntities(reg, inventoryapi.NewClient(nethttp.DefaultClient, cfg.GatewayURL, opts, intercept))
		}},
		{"main-data", func() error {
			return maindataapi.RegisterEntities(reg, maindataapi.NewClient(nethttp.DefaultClient, cfg.GatewayURL, opts, intercept))
		}},
		{"picking", func() error {
			return pickingapi.RegisterEntities(reg, pickingapi.NewClient(nethttp.DefaultClient, cfg.GatewayURL, opts, intercept))
		}},
		{"receiving", func() error {
			return receivingapi.RegisterEntities(reg, receivingapi.NewClient(nethttp.DefaultClient, cfg.GatewayURL, opts, intercept))
		}},
	}
	for _, r := range registrars {
		if err := r.fn(); err != nil {
			return nil, fmt.Errorf("register %s entities: %w", r.name, err)
		}
	}
	return reg, nil
}
