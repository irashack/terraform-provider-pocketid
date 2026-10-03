// Package valuefree makes configuration validation diagnostics value-free.
//
// A configured value can hold the provider's admin API key by mistake, and the
// provider never shows that key. The framework's validators, and some of this
// provider's own, repeat the configured value in their diagnostics, and report
// an element of a map or set at a path that names its key or value. Every
// validator in the provider's schemas is therefore wrapped (ResourceSchema,
// DataSourceSchema): it still runs, but each diagnostic it reports becomes a
// fixed sentence naming the attribute and the rule (the validator's own
// description), at a path that names no map key or set element. The
// configured value is never part of it.
package valuefree

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// describer is what every validator offers: a description of its rule.
type describer interface {
	Description(context.Context) string
}

// SafePath returns p up to its first map key or set element: those steps
// carry configured text. Attribute names and list indexes are kept.
func SafePath(p path.Path) path.Path {
	safe := path.Empty()
	for _, step := range p.Steps() {
		switch s := step.(type) {
		case path.PathStepAttributeName:
			safe = safe.AtName(string(s))
		case path.PathStepElementKeyInt:
			safe = safe.AtListIndex(int(s))
		default:
			return safe
		}
	}
	return safe
}

// rewrite turns the diagnostics a validator reported into value-free ones:
// each keeps its severity and summary (fixed text in every validator), gets
// the fixed detail, and is attached to the safe path. Repeats are dropped.
func rewrite(ctx context.Context, p path.Path, rule describer, reported diag.Diagnostics) diag.Diagnostics {
	if len(reported) == 0 {
		return nil
	}
	safe := SafePath(p)
	description := strings.TrimSuffix(strings.TrimSpace(rule.Description(ctx)), ".")
	format := "Attribute %s %s. The configured value is not shown."
	if description != "" && strings.ToUpper(description[:1]) == description[:1] {
		// A description that is a sentence of its own ("Ensure that ...").
		format = "Attribute %s: %s. The configured value is not shown."
	}
	detail := fmt.Sprintf(format, safe, description)
	var out diag.Diagnostics
	seen := map[string]bool{}
	for _, d := range reported {
		key := fmt.Sprint(d.Severity()) + "\x00" + d.Summary()
		if seen[key] {
			continue
		}
		seen[key] = true
		if d.Severity() == diag.SeverityWarning {
			out.AddAttributeWarning(safe, d.Summary(), detail)
		} else {
			out.AddAttributeError(safe, d.Summary(), detail)
		}
	}
	return out
}

type stringValidator struct{ inner validator.String }

// String wraps a string validator so that its diagnostics are value-free.
func String(inner validator.String) validator.String {
	if wrapped, ok := inner.(stringValidator); ok {
		return wrapped
	}
	return stringValidator{inner}
}

func (v stringValidator) Description(ctx context.Context) string { return v.inner.Description(ctx) }
func (v stringValidator) MarkdownDescription(ctx context.Context) string {
	return v.inner.MarkdownDescription(ctx)
}
func (v stringValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	inner := &validator.StringResponse{}
	v.inner.ValidateString(ctx, req, inner)
	resp.Diagnostics.Append(rewrite(ctx, req.Path, v.inner, inner.Diagnostics)...)
}

type listValidator struct{ inner validator.List }

// List wraps a list validator so that its diagnostics are value-free.
func List(inner validator.List) validator.List {
	if wrapped, ok := inner.(listValidator); ok {
		return wrapped
	}
	return listValidator{inner}
}

func (v listValidator) Description(ctx context.Context) string { return v.inner.Description(ctx) }
func (v listValidator) MarkdownDescription(ctx context.Context) string {
	return v.inner.MarkdownDescription(ctx)
}
func (v listValidator) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	inner := &validator.ListResponse{}
	v.inner.ValidateList(ctx, req, inner)
	resp.Diagnostics.Append(rewrite(ctx, req.Path, v.inner, inner.Diagnostics)...)
}

type setValidator struct{ inner validator.Set }

// Set wraps a set validator so that its diagnostics are value-free.
func Set(inner validator.Set) validator.Set {
	if wrapped, ok := inner.(setValidator); ok {
		return wrapped
	}
	return setValidator{inner}
}

func (v setValidator) Description(ctx context.Context) string { return v.inner.Description(ctx) }
func (v setValidator) MarkdownDescription(ctx context.Context) string {
	return v.inner.MarkdownDescription(ctx)
}
func (v setValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	inner := &validator.SetResponse{}
	v.inner.ValidateSet(ctx, req, inner)
	resp.Diagnostics.Append(rewrite(ctx, req.Path, v.inner, inner.Diagnostics)...)
}

type mapValidator struct{ inner validator.Map }

// Map wraps a map validator so that its diagnostics are value-free.
func Map(inner validator.Map) validator.Map {
	if wrapped, ok := inner.(mapValidator); ok {
		return wrapped
	}
	return mapValidator{inner}
}

func (v mapValidator) Description(ctx context.Context) string { return v.inner.Description(ctx) }
func (v mapValidator) MarkdownDescription(ctx context.Context) string {
	return v.inner.MarkdownDescription(ctx)
}
func (v mapValidator) ValidateMap(ctx context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	inner := &validator.MapResponse{}
	v.inner.ValidateMap(ctx, req, inner)
	resp.Diagnostics.Append(rewrite(ctx, req.Path, v.inner, inner.Diagnostics)...)
}

type int64Validator struct{ inner validator.Int64 }

// Int64 wraps a number validator so that its diagnostics are value-free (a
// static key can be all digits).
func Int64(inner validator.Int64) validator.Int64 {
	if wrapped, ok := inner.(int64Validator); ok {
		return wrapped
	}
	return int64Validator{inner}
}

func (v int64Validator) Description(ctx context.Context) string { return v.inner.Description(ctx) }
func (v int64Validator) MarkdownDescription(ctx context.Context) string {
	return v.inner.MarkdownDescription(ctx)
}
func (v int64Validator) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	inner := &validator.Int64Response{}
	v.inner.ValidateInt64(ctx, req, inner)
	resp.Diagnostics.Append(rewrite(ctx, req.Path, v.inner, inner.Diagnostics)...)
}

func wrapStrings(vs []validator.String) []validator.String {
	for i := range vs {
		vs[i] = String(vs[i])
	}
	return vs
}

func wrapLists(vs []validator.List) []validator.List {
	for i := range vs {
		vs[i] = List(vs[i])
	}
	return vs
}

func wrapSets(vs []validator.Set) []validator.Set {
	for i := range vs {
		vs[i] = Set(vs[i])
	}
	return vs
}

func wrapMaps(vs []validator.Map) []validator.Map {
	for i := range vs {
		vs[i] = Map(vs[i])
	}
	return vs
}

func wrapInt64s(vs []validator.Int64) []validator.Int64 {
	for i := range vs {
		vs[i] = Int64(vs[i])
	}
	return vs
}
