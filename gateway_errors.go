package musereelsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Gateway error codes are the HTTP SDK's only stable values for branching;
// human-readable messages are for diagnostics only.
const (
	GatewayInvalidInvocationRequest       = "invalid_invocation_request"
	GatewayModerationInvalidRequest       = "moderation_invalid_request"
	GatewayRuntimeUnauthenticated         = RuntimeUnauthenticated
	GatewayActorAssertionInvalid          = ActorAssertionInvalid
	GatewayActorAssertionReplayed         = ActorAssertionReplayed
	GatewayRuntimeForbidden               = "runtime_forbidden"
	GatewaySKUNotAllowed                  = "sku_not_allowed"
	GatewayComplianceRejected             = "compliance_rejected"
	GatewayInvocationNotFound             = "invocation_not_found"
	GatewayInvocationArtifactNotFound     = "invocation_artifact_not_found"
	GatewayInvocationArtifactExpired      = "invocation_artifact_expired"
	GatewayInvocationDeliveryModeMismatch = "invocation_delivery_mode_mismatch"
	GatewayInvocationIdempotencyConflict  = "invocation_idempotency_conflict"
	GatewayInvocationTransitionConflict   = "invocation_transition_conflict"
	GatewayInsufficientQuota              = "insufficient_quota"
	GatewayMemberLimitExceeded            = "member_limit_exceeded"
	GatewayRateLimited                    = "rate_limited"
	GatewayUpstreamUnavailable            = "upstream_unavailable"
	GatewayInternalError                  = "internal_error"
)

// GatewayError is the stable shape of a Gateway HTTP error. Retryable
// preserves the wire value for diagnostics; SDK-created errors use the frozen
// code table as their default.
//
// Call RetryableByCode or IsRetryable for retry decisions; do not call
// RetryableGatewayCode directly when you have a GatewayError. The two once
// happened to be equivalent, but they are not now: RetryableGatewayCode takes
// only a code string and cannot see this response's retryable value, so it
// always returns true for internal_error. The contract (06:611-619) says the
// retryable value of internal_error is not a constant: a deterministic
// deployment-configuration failure arrives as internal_error with
// retryable=false, and the caller must stop retrying. Only RetryableByCode and
// IsRetryable can see the wire value. RetryableGatewayCode remains for the
// conservative default when the caller has only a code string.
type GatewayError struct {
	Code    string `json:"code"`
	Message string `json:"message"`

	// Retryable is a direct read of the wire value for diagnostics; do not use it
	// to decide whether to retry. When the server omits retryable, this field is
	// false, while the contract's conservative default for an unregistered
	// internal code is true (06:611-619). This one field therefore gives the
	// opposite answer to the effective decision. Always use RetryableByCode or
	// IsRetryable: only they can see whether the wire carried the field at all.
	Retryable    bool           `json:"retryable"`
	RetryAfterMS *int64         `json:"retry_after_ms"`
	Details      map[string]any `json:"details"`

	HTTPStatus   int    `json:"-"`
	RequestID    string `json:"-"`
	InvocationID string `json:"-"`

	retryableFromWire bool
}

// Error intentionally omits Message so that server diagnostic text cannot
// accidentally carry a full token or assertion into an error string.
func (err GatewayError) Error() string {
	code := err.Code
	if code == "" {
		code = GatewayInternalError
	}
	if err.HTTPStatus > 0 {
		return "gateway error " + code + " (HTTP " + strconv.Itoa(err.HTTPStatus) + ")"
	}
	return "gateway error " + code
}

// ErrorCode implements the SDK's ErrorCodeProvider error shape.
func (err GatewayError) ErrorCode() string { return err.Code }

// RetryableGatewayCode returns the frozen code table's default retryability
// decision.
//
// It cannot see this response's wire retryable value, so it always returns true
// for internal_error. When a *GatewayError is available, use RetryableByCode
// instead; see GatewayError for the distinction.
func RetryableGatewayCode(code string) bool {
	switch code {
	case GatewayRateLimited, GatewayUpstreamUnavailable, GatewayInternalError:
		return true
	case GatewayInvalidInvocationRequest,
		GatewayModerationInvalidRequest,
		GatewayRuntimeUnauthenticated,
		GatewayActorAssertionInvalid,
		GatewayActorAssertionReplayed,
		GatewayRuntimeForbidden,
		GatewaySKUNotAllowed,
		GatewayComplianceRejected,
		GatewayInvocationNotFound,
		GatewayInvocationArtifactNotFound,
		GatewayInvocationArtifactExpired,
		GatewayInvocationDeliveryModeMismatch,
		GatewayInvocationIdempotencyConflict,
		GatewayInvocationTransitionConflict,
		GatewayInsufficientQuota,
		GatewayMemberLimitExceeded:
		return false
	default:
		return false
	}
}

// RetryableByCode returns the effective retryability decision for an invocation
// error without changing the GatewayError.Retryable field. For an internal_error
// received over HTTP, an explicit server value is used instead of the code-table
// default.
func (err GatewayError) RetryableByCode() bool {
	if err.Code == GatewayInternalError && err.retryableFromWire {
		return err.Retryable
	}
	// 🔴 拿到了 invocation_id 就说明 invocation **已经落库**（202 之后才可能解出它）。
	// 此时把 create 再发一次不是"重试"，是对同一次逻辑调用的**第二次上游请求**——
	// 换了幂等键就真的会打两枪，正是 06:542-543 要避免的盲目重提。
	// 正确动作是拿这个 ID 去 Get/Poll，所以这里判不可重试。
	//
	// 只在服务端**没有**显式给 retryable 时才由这条兜底：显式值仍然压过它（上一个分支先返回）。
	if err.Code == GatewayInternalError && err.InvocationID != "" {
		return false
	}
	return RetryableGatewayCode(err.Code)
}

// IsRetryable is the explicit method form of RetryableByCode.
func (err GatewayError) IsRetryable() bool {
	return err.RetryableByCode()
}

type gatewayErrorWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Retryable 是指针而不是 bool：要分得清「服务端说了 false」与「服务端根本没说」。
	// 06:611-619 把未登记内部码的缺省定为**保守可重试**，所以缺字段时不能落成
	// bool 零值 false —— 那会让调用方对一个瞬时内部错过早放弃。
	Retryable    *bool           `json:"retryable"`
	RetryAfterMS *int64          `json:"retry_after_ms"`
	Details      json.RawMessage `json:"details"`
}

// UnmarshalJSON preserves numeric values in Details without loss by using
// json.Number and extracts InvocationID from the frozen details field.
func (err *GatewayError) UnmarshalJSON(data []byte) error {
	var wire gatewayErrorWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	details, detailsErr := decodeGatewayDetails(wire.Details)
	if detailsErr != nil {
		return detailsErr
	}
	retryable := false
	if wire.Retryable != nil {
		retryable = *wire.Retryable
	}
	*err = GatewayError{
		Code:         wire.Code,
		Message:      wire.Message,
		Retryable:    retryable,
		RetryAfterMS: wire.RetryAfterMS,
		Details:      details,
		InvocationID: gatewayInvocationIDFromDetails(details),

		// 只有服务端**显式**给了 retryable，它才有资格压过码表。
		// ⚠ 这一行必须留在整体赋值里：*err = GatewayError{...} 会重置每个未导出字段，
		// 在它之后赋值才有效，在它之前赋值会被抹掉。
		retryableFromWire: wire.Retryable != nil,
	}
	return nil
}

func decodeGatewayDetails(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var details map[string]any
	if err := decoder.Decode(&details); err != nil {
		return nil, err
	}
	if details == nil {
		return nil, fmt.Errorf("gateway error details is not an object")
	}
	return details, nil
}

func gatewayInvocationIDFromDetails(details map[string]any) string {
	if details == nil {
		return ""
	}
	invocationID, _ := details["invocation_id"].(string)
	return invocationID
}

func isGatewayInvocationErrorCode(code string) bool {
	switch code {
	case GatewayInvalidInvocationRequest,
		GatewayModerationInvalidRequest, // moderation_invalid_request
		GatewayRuntimeUnauthenticated,
		GatewayActorAssertionInvalid,
		GatewayActorAssertionReplayed,
		GatewayRuntimeForbidden,
		GatewaySKUNotAllowed,
		GatewayComplianceRejected,
		GatewayInvocationNotFound,
		GatewayInvocationArtifactNotFound,
		GatewayInvocationArtifactExpired,
		GatewayInvocationDeliveryModeMismatch,
		GatewayInvocationIdempotencyConflict,
		GatewayInvocationTransitionConflict, // invocation_transition_conflict
		GatewayInsufficientQuota,
		GatewayMemberLimitExceeded,
		GatewayRateLimited,
		GatewayUpstreamUnavailable,
		GatewayInternalError:
		return true
	default:
		return false
	}
}

func newGatewayError(code string, status int, requestID, message string) *GatewayError {
	return &GatewayError{
		Code:       code,
		Message:    message,
		Retryable:  RetryableGatewayCode(code),
		HTTPStatus: status,
		RequestID:  requestID,
	}
}

func newGatewayProtocolError(status int) *GatewayError {
	return newGatewayError(GatewayInternalError, status, "", "gateway response did not match the frozen contract")
}

func newGatewayProtocolErrorWithInvocationID(status int, invocationID string) *GatewayError {
	err := newGatewayProtocolError(status)
	err.InvocationID = invocationID
	return err
}

func gatewayErrorFromBytes(body []byte, status int, allowed func(string) bool) *GatewayError {
	var envelope struct {
		RequestID string          `json:"request_id"`
		Error     json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Error) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return newGatewayError(GatewayInternalError, status, "", "gateway error envelope is invalid")
	}
	var gatewayErr GatewayError
	if err := json.Unmarshal(envelope.Error, &gatewayErr); err != nil {
		return newGatewayError(GatewayInternalError, status, envelope.RequestID, "gateway error envelope is invalid")
	}
	if allowed == nil || !allowed(gatewayErr.Code) {
		gatewayErr.Code = GatewayInternalError
		gatewayErr.Message = "unrecognized gateway error code"
	}
	gatewayErr.HTTPStatus = status
	gatewayErr.RequestID = envelope.RequestID
	if gatewayErr.InvocationID == "" {
		gatewayErr.InvocationID = gatewayInvocationIDFromDetails(gatewayErr.Details)
	}
	return &gatewayErr
}
