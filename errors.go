package musereelsdk

import (
	"errors"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

const runtimeErrorDomain = "sluice.runtime"

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
		if code := provider.ErrorCode(); code != "" {
			return code
		}
	}
	if info, ok := runtimeErrorInfo(err); ok && info.Reason != "" {
		return info.Reason
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

func runtimeErrorInfo(err error) (*errdetails.ErrorInfo, bool) {
	if err == nil {
		return nil, false
	}
	for _, detail := range status.Convert(err).Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info == nil || info.Domain != runtimeErrorDomain {
			continue
		}
		return info, true
	}
	return nil, false
}

// IsRuntimeUnauthenticated reports only the stable runtime code. A generic
// codes.Unauthenticated status is deliberately insufficient to trigger a
// credential refresh.
func IsRuntimeUnauthenticated(err error) bool {
	return ErrorCode(err) == RuntimeUnauthenticated
}
