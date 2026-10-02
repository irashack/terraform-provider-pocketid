package datasources

import (
	"context"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAll_SortedAndUnique(t *testing.T) {
	var names []string
	for _, factory := range All() {
		first := factory()
		require.NotNil(t, first)
		var resp datasource.MetadataResponse
		first.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "pocketid"}, &resp)
		names = append(names, resp.TypeName)
	}
	assert.NotEmpty(t, names)
	assert.True(t, sort.StringsAreSorted(names), "%v", names)
	assert.Len(t, names, len(registry), "every registered data source is returned once")
}

type misnamedDataSource struct{ datasource.DataSource }

func (misnamedDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "other_thing"
}

func TestRegister_RefusesMistakes(t *testing.T) {
	before := len(registry)
	assert.PanicsWithValue(t, "datasources: pocketid_client is registered twice", func() { register(NewClientDataSource) })
	assert.Panics(t, func() { register(nil) })
	assert.Panics(t, func() { register(func() datasource.DataSource { return misnamedDataSource{} }) })
	assert.Len(t, registry, before, "a refused registration adds nothing")
}
