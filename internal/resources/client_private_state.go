package resources

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// privateGetter and privateSetter are the parts of the framework's private
// state data (resource requests' and responses' Private) this resource uses.
type privateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// privateFlag reports whether a private-state flag is set. A missing private
// state reads as unset.
func privateFlag(ctx context.Context, private privateGetter, key string) bool {
	if private == nil {
		return false
	}
	value, _ := private.GetKey(ctx, key)
	return string(value) == "true"
}

// setPrivateFlag sets or removes a private-state flag.
func setPrivateFlag(ctx context.Context, private privateSetter, key string, on bool) diag.Diagnostics {
	if on {
		return private.SetKey(ctx, key, []byte("true"))
	}
	return private.SetKey(ctx, key, nil)
}
