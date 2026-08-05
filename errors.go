package musereelsdk

import (
	"errors"
	"strings"

	"google.golang.org/grpc/status"
)

// Stable runtime error codes frozen by the SDK-002 contract. Branches in the
// SDK use these codes, not unstable human-readable status text or transport
// status alone.
const (
	RuntimeUnauthenticated = "runtime_unauthenticated"
	ActorAssertionInvalid  = "actor_assertion_invalid"
	ActorAssertionReplayed = "actor_assertion_replayed"
)

// ErrorCodeProvider is an optional application error shape for stable Sluice
// codes. grpc status errors are also recognized when their status message is
// exactly a frozen code or starts with that code followed by a delimiter.
type ErrorCodeProvider interface {
	ErrorCode() string
}

// ErrorCode returns a frozen Sluice error code when one is present.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var provider ErrorCodeProvider
	if errors.As(err, &provider) {
		return provider.ErrorCode()
	}
	message := status.Convert(err).Message()
	for _, code := range []string{
		RuntimeUnauthenticated,
		ActorAssertionInvalid,
		ActorAssertionReplayed,
	} {
		if message == code || strings.HasPrefix(message, code+":") || strings.HasPrefix(message, code+" ") {
			return code
		}
	}
	return ""
}

// IsRuntimeUnauthenticated reports only the stable runtime code. A generic
// codes.Unauthenticated status is deliberately insufficient to trigger a
// credential refresh.
func IsRuntimeUnauthenticated(err error) bool {
	return ErrorCode(err) == RuntimeUnauthenticated
}
