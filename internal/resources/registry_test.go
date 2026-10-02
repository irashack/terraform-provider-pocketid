package resources

import (
	"context"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAll_SortedAndUnique(t *testing.T) {
	var names []string
	for _, factory := range All() {
		first := factory()
		require.NotNil(t, first)
		var resp resource.MetadataResponse
		first.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "pocketid"}, &resp)
		names = append(names, resp.TypeName)
	}
	assert.NotEmpty(t, names)
	assert.True(t, sort.StringsAreSorted(names), "%v", names)
	assert.Len(t, names, len(registry), "every registered resource is returned once")
}

type misnamedResource struct{ resource.Resource }

func (misnamedResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "other_thing"
}

func TestRegister_RefusesMistakes(t *testing.T) {
	before := len(registry)
	assert.PanicsWithValue(t, "resources: pocketid_client is registered twice", func() { register(NewClientResource) })
	assert.Panics(t, func() { register(nil) })
	assert.Panics(t, func() { register(func() resource.Resource { return misnamedResource{} }) })
	assert.Len(t, registry, before, "a refused registration adds nothing")
}
