package musereelsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Gateway 错误码是 HTTP SDK 唯一用于分支判断的稳定值，人类可读消息只作诊断。
const (
	GatewayInvalidInvocationRequest       = "invalid_invocation_request"
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
	GatewayInsufficientQuota              = "insufficient_quota"
	GatewayMemberLimitExceeded            = "member_limit_exceeded"
	GatewayRateLimited                    = "rate_limited"
	GatewayUpstreamUnavailable            = "upstream_unavailable"
	GatewayInternalError                  = "internal_error"

	GatewayInvalidRegistration     = "invalid_registration"
	GatewaySiteContextInvalid      = "site_context_invalid"
	GatewayRegistrationUnavailable = "registration_unavailable"
)

// GatewayError 是 Gateway HTTP 返回的稳定错误形状。Retryable 保留 wire 原值供诊断；
// 重试判断必须调用 RetryableGatewayCode 或 RetryableByCode，invocation 以冻结三码表为准。
type GatewayError struct {
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable"`
	RetryAfterMS *int64         `json:"retry_after_ms"`
	Details      map[string]any `json:"details"`

	HTTPStatus   int    `json:"-"`
	RequestID    string `json:"-"`
	InvocationID string `json:"-"`
}

// Error 故意不包含 Message，避免服务端诊断文本意外带入 token 或 assertion 全文。
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

// ErrorCode 对齐既有 SDK 错误面的 ErrorCodeProvider。
func (err GatewayError) ErrorCode() string { return err.Code }

// RetryableGatewayCode 返回冻结的 invocation retryable 三码判定。
func RetryableGatewayCode(code string) bool {
	switch code {
	case GatewayRateLimited, GatewayUpstreamUnavailable, GatewayInternalError:
		return true
	default:
		return false
	}
}

// RetryableByCode 返回 invocation 错误的有效 retryable 判定；服务端值矛盾时不改写
// GatewayError.Retryable 原字段。
func (err GatewayError) RetryableByCode() bool {
	return RetryableGatewayCode(err.Code)
}

// IsRetryable 是 RetryableByCode 的显式方法形式。registration 有独立冻结契约：
// registration_unavailable 携带 retryable=true，但不属于 invocation 三码表。
func (err GatewayError) IsRetryable() bool {
	if err.Code == GatewayRegistrationUnavailable {
		return err.Retryable
	}
	return RetryableGatewayCode(err.Code)
}

type gatewayErrorWire struct {
	Code         string          `json:"code"`
	Message      string          `json:"message"`
	Retryable    bool            `json:"retryable"`
	RetryAfterMS *int64          `json:"retry_after_ms"`
	Details      json.RawMessage `json:"details"`
}

// UnmarshalJSON 用 json.Number 无损保留 Details 数值，并从冻结 details 字段提取 InvocationID。
func (err *GatewayError) UnmarshalJSON(data []byte) error {
	var wire gatewayErrorWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	details, detailsErr := decodeGatewayDetails(wire.Details)
	if detailsErr != nil {
		return detailsErr
	}
	*err = GatewayError{
		Code:         wire.Code,
		Message:      wire.Message,
		Retryable:    wire.Retryable,
		RetryAfterMS: wire.RetryAfterMS,
		Details:      details,
		InvocationID: gatewayInvocationIDFromDetails(details),
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

func isGatewaySiteContextErrorCode(code string) bool {
	switch code {
	case GatewayInvalidRegistration, GatewaySiteContextInvalid,
		GatewayRegistrationUnavailable, GatewayInternalError:
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
