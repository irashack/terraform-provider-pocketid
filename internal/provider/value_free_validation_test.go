package provider_test

import (
	"context"
	"math/big"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pocketidprovider "github.com/irashack/terraform-provider-pocketid/internal/provider"
)

// The synthetic key put into every configurable attribute. The text around it
// makes the value fail every rule a validator could check (a UUID, a URL, a
// duration, a length limit, a fixed choice, an e-mail address); the number
// is an all-digit key, which a static key can be.
const (
	valueFreeKey    = "zzSyntheticAdminKeyMarker0123"
	valueFreeNumber = "4815162342108151"
)

var valueFreeText = valueFreeKey + strings.Repeat("x", 400) + " /#?%\"\\ é"

// Validation never shows a configured value: with the synthetic key in any
// configurable attribute of any resource or data source (a string, a number,
// an element of a list or set, a map key, an attribute of a nested object),
// no diagnostic of ValidateResourceConfig or ValidateDataResourceConfig
// repeats it, in its summary, its detail or its attribute path.
func TestValidationDiagnosticsNeverShowConfiguredValues(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(pocketidprovider.New("test")())()
	require.NoError(t, err)
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	require.NoError(t, err)

	failures := 0
	check := func(t *testing.T, what string, diags []*tfprotov6.Diagnostic) {
		t.Helper()
		for _, d := range diags {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				failures++
			}
			text := d.Summary + "\n" + d.Detail
			if d.Attribute != nil {
				text += "\n" + d.Attribute.String()
			}
			assert.NotContains(t, text, valueFreeKey, what)
			assert.NotContains(t, text, valueFreeNumber, what)
		}
	}

	for _, kind := range []struct {
		name     string
		schemas  map[string]*tfprotov6.Schema
		validate func(typeName string, config *tfprotov6.DynamicValue) []*tfprotov6.Diagnostic
	}{
		{"resource", schemas.ResourceSchemas, func(typeName string, config *tfprotov6.DynamicValue) []*tfprotov6.Diagnostic {
			resp, err := server.ValidateResourceConfig(ctx, &tfprotov6.ValidateResourceConfigRequest{TypeName: typeName, Config: config})
			require.NoError(t, err)
			return resp.Diagnostics
		}},
		{"data source", schemas.DataSourceSchemas, func(typeName string, config *tfprotov6.DynamicValue) []*tfprotov6.Diagnostic {
			resp, err := server.ValidateDataResourceConfig(ctx, &tfprotov6.ValidateDataResourceConfigRequest{TypeName: typeName, Config: config})
			require.NoError(t, err)
			return resp.Diagnostics
		}},
	} {
		names := make([]string, 0, len(kind.schemas))
		for name := range kind.schemas {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, typeName := range names {
			s := kind.schemas[typeName]
			objectType := s.ValueType().(tftypes.Object)
			for _, attribute := range s.Block.Attributes {
				if attribute.Computed && !attribute.Optional && !attribute.Required {
					continue
				}
				for i, value := range valueFreeCandidates(objectType.AttributeTypes[attribute.Name], attribute) {
					config := valueFreeObject(objectType, map[string]tftypes.Value{attribute.Name: value})
					dynamic, err := tfprotov6.NewDynamicValue(objectType, config)
					require.NoError(t, err)
					what := kind.name + " " + typeName + "." + attribute.Name
					t.Run(what, func(t *testing.T) {
						_ = i
						check(t, what, kind.validate(typeName, &dynamic))
					})
				}
			}
		}
	}
	assert.Positive(t, failures, "the synthetic values fail validation somewhere, so the check is not vacuous")
}

// valueFreeCandidates returns values of typ that carry the synthetic key in
// every position the type allows: the value itself, a list or set element,
// a map key, or an attribute of a nested object (one value per nested
// attribute, the others null).
func valueFreeCandidates(typ tftypes.Type, attribute *tfprotov6.SchemaAttribute) []tftypes.Value {
	switch {
	case typ.Is(tftypes.String):
		return []tftypes.Value{tftypes.NewValue(tftypes.String, valueFreeText)}
	case typ.Is(tftypes.Number):
		number, _ := new(big.Float).SetString("-" + valueFreeNumber)
		return []tftypes.Value{tftypes.NewValue(tftypes.Number, number)}
	case typ.Is(tftypes.Bool):
		return nil
	}
	var nested []*tfprotov6.SchemaAttribute
	if attribute != nil && attribute.NestedType != nil {
		nested = attribute.NestedType.Attributes
	}
	switch t := typ.(type) {
	case tftypes.List:
		var out []tftypes.Value
		for _, element := range valueFreeElements(t.ElementType, nested) {
			out = append(out, tftypes.NewValue(t, []tftypes.Value{element}))
		}
		return out
	case tftypes.Set:
		var out []tftypes.Value
		for _, element := range valueFreeElements(t.ElementType, nested) {
			out = append(out, tftypes.NewValue(t, []tftypes.Value{element}))
		}
		return out
	case tftypes.Map:
		var out []tftypes.Value
		elements := valueFreeElements(t.ElementType, nested)
		if t.ElementType.Is(tftypes.String) {
			elements = append(elements, tftypes.NewValue(tftypes.String, ""))
		}
		for _, element := range elements {
			out = append(out, tftypes.NewValue(t, map[string]tftypes.Value{valueFreeText: element}))
		}
		return out
	case tftypes.Object:
		return valueFreeElements(t, nested)
	}
	return nil
}

// valueFreeElements returns the element values for a collection: the
// candidates of a primitive element type, or one object per nested attribute
// carrying the key in that attribute.
func valueFreeElements(typ tftypes.Type, nested []*tfprotov6.SchemaAttribute) []tftypes.Value {
	object, ok := typ.(tftypes.Object)
	if !ok {
		return valueFreeCandidates(typ, nil)
	}
	var out []tftypes.Value
	for _, attribute := range nested {
		if attribute.Computed && !attribute.Optional && !attribute.Required {
			continue
		}
		for _, value := range valueFreeCandidates(object.AttributeTypes[attribute.Name], attribute) {
			out = append(out, valueFreeObject(object, map[string]tftypes.Value{attribute.Name: value}))
		}
	}
	return out
}

// valueFreeObject returns an object of typ whose attributes are null except
// those in set.
func valueFreeObject(typ tftypes.Object, set map[string]tftypes.Value) tftypes.Value {
	values := make(map[string]tftypes.Value, len(typ.AttributeTypes))
	for name, attributeType := range typ.AttributeTypes {
		if value, ok := set[name]; ok {
			values[name] = value
			continue
		}
		values[name] = tftypes.NewValue(attributeType, nil)
	}
	return tftypes.NewValue(typ, values)
}
