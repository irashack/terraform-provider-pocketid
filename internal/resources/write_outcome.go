package resources

import (
	"errors"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// writeRefused reports whether a failed write certainly changed nothing: the
// server refused it with a 4xx (client.IsDefiniteRejection), or the client
// refused to send it (an identifier or parameter it would not send,
// client.ErrInvalidIdentifier). An error that also wraps
// client.ErrResultUnread is never a refusal, whatever else it carries: the
// server accepted the write and only its result could not be used (an
// unusable ID in the answer, a 403 on the read-back). Every resource
// classifies a failed write with this, so that order holds everywhere.
func writeRefused(err error) bool {
	if err == nil || errors.Is(err, client.ErrResultUnread) {
		return false
	}
	return client.IsDefiniteRejection(err) || errors.Is(err, client.ErrInvalidIdentifier)
}
