package datasources

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

// registryProviderType is the provider type name used to ask each data
// source for its full type name while registering it. Only the order and
// uniqueness of the names matter here; the provider supplies the real
// prefix when Terraform asks.
const registryProviderType = "pocketid"

var (
	registryMu sync.Mutex
	registry   = map[string]func() datasource.DataSource{}
)

// register adds a data source to the provider. Each data-source file calls it
// from its own init function, so adding a data source touches no other file:
//
//	func init() { register(NewExampleDataSource) }
//
// It panics on a nil constructor, an empty type name or a type name that is
// already registered: each is a programming error that must fail every test.
func register(factory func() datasource.DataSource) {
	if factory == nil {
		panic("datasources: register called with a nil constructor")
	}
	var response datasource.MetadataResponse
	factory().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: registryProviderType}, &response)
	name := response.TypeName
	if !strings.HasPrefix(name, registryProviderType+"_") {
		panic(fmt.Sprintf("datasources: registered data source has type name %q, want a %s_ prefix", name, registryProviderType))
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("datasources: %s is registered twice", name))
	}
	registry[name] = factory
}

// All returns the constructor of every registered data source, sorted by type
// name so the order is the same on every run.
func All() []func() datasource.DataSource {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	factories := make([]func() datasource.DataSource, 0, len(names))
	for _, name := range names {
		factories = append(factories, registry[name])
	}
	return factories
}
