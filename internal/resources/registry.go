package resources

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// registryProviderType is the provider type name used to ask each resource
// for its full type name while registering it. Only the order and
// uniqueness of the names matter here; the provider supplies the real
// prefix when Terraform asks.
const registryProviderType = "pocketid"

var (
	registryMu sync.Mutex
	registry   = map[string]func() resource.Resource{}
)

// register adds a resource to the provider. Each resource file calls it from
// its own init function, so adding a resource touches no other file:
//
//	func init() { register(NewExampleResource) }
//
// It panics on a nil constructor, an empty type name or a type name that is
// already registered: each is a programming error that must fail every test.
func register(factory func() resource.Resource) {
	if factory == nil {
		panic("resources: register called with a nil constructor")
	}
	var response resource.MetadataResponse
	factory().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: registryProviderType}, &response)
	name := response.TypeName
	if !strings.HasPrefix(name, registryProviderType+"_") {
		panic(fmt.Sprintf("resources: registered resource has type name %q, want a %s_ prefix", name, registryProviderType))
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("resources: %s is registered twice", name))
	}
	registry[name] = factory
}

// All returns the constructor of every registered resource, sorted by type
// name so the order is the same on every run.
func All() []func() resource.Resource {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	factories := make([]func() resource.Resource, 0, len(names))
	for _, name := range names {
		factories = append(factories, registry[name])
	}
	return factories
}
