package valuefree

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// DataSourceSchema returns s with every string, list, set, map and number
// validator wrapped (see the package documentation), nested attributes
// included. Call it on the schema a data source's Schema method returns.
func DataSourceSchema(s schema.Schema) schema.Schema {
	s.Attributes = dataSourceAttributes(s.Attributes)
	return s
}

func dataSourceAttributes(attributes map[string]schema.Attribute) map[string]schema.Attribute {
	for name, attribute := range attributes {
		switch a := attribute.(type) {
		case schema.StringAttribute:
			a.Validators = wrapStrings(a.Validators)
			attributes[name] = a
		case schema.ListAttribute:
			a.Validators = wrapLists(a.Validators)
			attributes[name] = a
		case schema.SetAttribute:
			a.Validators = wrapSets(a.Validators)
			attributes[name] = a
		case schema.MapAttribute:
			a.Validators = wrapMaps(a.Validators)
			attributes[name] = a
		case schema.Int64Attribute:
			a.Validators = wrapInt64s(a.Validators)
			attributes[name] = a
		case schema.ListNestedAttribute:
			a.Validators = wrapLists(a.Validators)
			a.NestedObject.Attributes = dataSourceAttributes(a.NestedObject.Attributes)
			attributes[name] = a
		case schema.SetNestedAttribute:
			a.Validators = wrapSets(a.Validators)
			a.NestedObject.Attributes = dataSourceAttributes(a.NestedObject.Attributes)
			attributes[name] = a
		case schema.MapNestedAttribute:
			a.Validators = wrapMaps(a.Validators)
			a.NestedObject.Attributes = dataSourceAttributes(a.NestedObject.Attributes)
			attributes[name] = a
		case schema.SingleNestedAttribute:
			a.Attributes = dataSourceAttributes(a.Attributes)
			attributes[name] = a
		}
	}
	return attributes
}
