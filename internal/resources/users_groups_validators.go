package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// usersGroupsRuneLength checks a string's length in characters (Unicode code
// points), the way Pocket ID's min and max validators count it; the
// framework's length validators count bytes.
type usersGroupsRuneLength struct{ min, max int }

func (v usersGroupsRuneLength) Description(context.Context) string {
	if v.min > 0 {
		return fmt.Sprintf("must be %d to %d characters long", v.min, v.max)
	}
	return fmt.Sprintf("must be at most %d characters long", v.max)
}

func (v usersGroupsRuneLength) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v usersGroupsRuneLength) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := utf8.RuneCountInString(req.ConfigValue.ValueString()); n < v.min || n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid length",
			fmt.Sprintf("%s %s (Pocket ID's limit); it is %d.", req.Path, v.Description(ctx), n))
	}
}

// usernamePattern is Pocket ID's username rule (dto.validateUsernameRegex,
// unchanged from 2.14.0 to 2.17.0): letters, digits, '_', '.', '@' and '-',
// starting and ending with a letter or digit.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_.@-]*[a-zA-Z0-9])?$`)

func usernameValidators() []validator.String {
	return []validator.String{
		usersGroupsRuneLength{min: 1, max: 50},
		stringvalidator.RegexMatches(usernamePattern,
			"must contain only letters, digits, '_', '.', '@' and '-', and start and end with a letter or digit (Pocket ID's rule)"),
	}
}

// reservedClaimKeys are the claim names Pocket ID refuses for custom claims
// (service.isReservedClaim, 2.14.0 to 2.17.0; "type" is TokenTypeClaim). The
// comparison is case-sensitive, as on the server.
var reservedClaimKeys = []string{
	"given_name", "family_name", "name", "email", "email_verified", "preferred_username",
	"display_name", "groups", "type", "sub", "iss", "aud", "exp", "iat", "auth_time",
	"nonce", "acr", "amr", "azp", "client_id", "nbf", "jti",
}

// customClaimsValidators apply Pocket ID's rules for custom claims at plan
// time: a key and a value are required (non-empty) and a key must not be
// reserved. Failures are reported on the map, naming no key (see
// customClaimsValidator).
func customClaimsValidators() []validator.Map {
	return []validator.Map{customClaimsValidator{}}
}

// oneTimeTokenTTLValidator checks a one-time access token ttl the way Pocket
// ID does (dto "ttl" validator): a Go duration greater than 1 second and at
// most 31 days.
type oneTimeTokenTTLValidator struct{}

func (oneTimeTokenTTLValidator) Description(context.Context) string {
	return "must be a Go duration (such as 15m or 24h) greater than 1s and at most 744h (31 days)"
}

func (v oneTimeTokenTTLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v oneTimeTokenTTLValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := checkOneTimeTokenTTL(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid ttl", err.Error())
	}
}

// checkOneTimeTokenTTL returns an error unless ttl is a duration Pocket ID
// accepts for a one-time access token.
func checkOneTimeTokenTTL(ttl string) error {
	d, err := time.ParseDuration(ttl)
	if err != nil {
		return errors.New("the ttl value must be a Go duration string such as \"15m\" or \"1h\"")
	}
	if d <= time.Second || d > maxOneTimeAccessTokenTTL {
		return errors.New("the ttl must be greater than 1 second and at most 744h (31 days)")
	}
	return nil
}
