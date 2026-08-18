//go:build conformance

package conformance

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/emiya-dev/musereel-sdk"
	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	textGenerateSKU       = "text.generate.v1"
	moderationGenerateSKU = "moderation.generate.v1"
	videoGenerateSKU      = "video.generate.v1"
	imageGenerateSKU      = "image.generate.v1"
	lyricsGenerateSKU     = "lyrics.generate.v1"
	musicGenerateSKU      = "music.generate.v1"
	speechGenerateSKU     = "speech.generate.v1"

	conformanceSchemaVersionOne   = "1"
	conformanceVideoSchemaVersion = "3"
)

var conformanceSchemaVersionBySKU = map[string]string{
	textGenerateSKU:       conformanceSchemaVersionOne,
	moderationGenerateSKU: conformanceSchemaVersionOne,
	videoGenerateSKU:      conformanceVideoSchemaVersion,
	imageGenerateSKU:      conformanceSchemaVersionOne,
	lyricsGenerateSKU:     conformanceSchemaVersionOne,
	musicGenerateSKU:      conformanceSchemaVersionOne,
	speechGenerateSKU:     conformanceSchemaVersionOne,
}

func defaultSchemaVersionForSKU(skuID string) (string, error) {
	schemaVersion, ok := conformanceSchemaVersionBySKU[skuID]
	if !ok {
		return "", fmt.Errorf("MUSEREEL_CONFORMANCE_SKU_ID 必须是 gateway capability 串，未知值 %q", skuID)
	}
	return schemaVersion, nil
}

func resolveConformanceSchemaVersion(skuID, override string) (string, error) {
	defaultVersion, err := defaultSchemaVersionForSKU(skuID)
	if err != nil {
		return "", err
	}
	if override = strings.TrimSpace(override); override != "" {
		return override, nil
	}
	return defaultVersion, nil
}

// config 是 E14 compose 提供的 conformance 运行输入。
type config struct {
	gatewayURL    string
	runtimeTarget string
	mtls          sdk.MTLSConfig
	signingKey    string
	signingKID    string
	instanceID    string
	tenantID      string
	sessionID     string
	actor         string
	skuID         string
	schemaVersion string
	taskRef       string
	deliveryMode  sdk.GatewayDeliveryMode
	input         json.RawMessage
	parameters    json.RawMessage
	eventID       string
}

// loadConfig 缺环境时必须返回明确失败，不能把真实 conformance 变成 skip。
func loadConfig() (config, error) {
	values := make(map[string]string)
	missing := make([]string, 0)
	for _, name := range []string{
		"MUSEREEL_CONFORMANCE_GATEWAY_URL",
		"MUSEREEL_CONFORMANCE_RUNTIME_TARGET",
		"MUSEREEL_CONFORMANCE_MTLS_CERT_FILE",
		"MUSEREEL_CONFORMANCE_MTLS_KEY_FILE",
		"MUSEREEL_CONFORMANCE_MTLS_CA_FILE",
		"MUSEREEL_CONFORMANCE_SIGNING_PRIVATE_KEY_FILE",
		"MUSEREEL_CONFORMANCE_SIGNING_KID",
		"MUSEREEL_CONFORMANCE_INSTANCE_ID",
		"MUSEREEL_CONFORMANCE_TENANT_ID",
		"MUSEREEL_CONFORMANCE_SESSION_ID",
		"MUSEREEL_CONFORMANCE_ACTOR",
		"MUSEREEL_CONFORMANCE_SKU_ID",
		"MUSEREEL_CONFORMANCE_TASK_REF",
		"MUSEREEL_CONFORMANCE_DELIVERY_MODE",
	} {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			missing = append(missing, name)
			continue
		}
		values[name] = value
	}
	if len(missing) != 0 {
		return config{}, fmt.Errorf("需要 sluice compose 环境：缺少 conformance env: %s", strings.Join(missing, ", "))
	}

	deliveryMode := sdk.GatewayDeliveryMode(values["MUSEREEL_CONFORMANCE_DELIVERY_MODE"])
	if deliveryMode != sdk.GatewayDeliveryAsync && deliveryMode != sdk.GatewayDeliveryStream {
		return config{}, fmt.Errorf("需要 sluice compose 环境：MUSEREEL_CONFORMANCE_DELIVERY_MODE 必须是 async 或 stream")
	}

	skuID := values["MUSEREEL_CONFORMANCE_SKU_ID"]
	schemaVersion, err := resolveConformanceSchemaVersion(
		skuID,
		os.Getenv("MUSEREEL_CONFORMANCE_SPEC_SCHEMA_VERSION"),
	)
	if err != nil {
		return config{}, err
	}
	defaultInput, defaultParameters, err := buildConformanceSpec(skuID, schemaVersion)
	if err != nil {
		return config{}, err
	}
	input := json.RawMessage(strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_SPEC_INPUT_JSON")))
	if len(input) == 0 {
		input = defaultInput
	}
	parameters := json.RawMessage(strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_SPEC_PARAMETERS_JSON")))
	if len(parameters) == 0 {
		parameters = defaultParameters
	}
	eventID := strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_EVENT_ID"))
	if eventID == "" {
		eventID = fmt.Sprintf("sdk005-conformance-%d", time.Now().UnixNano())
	}

	return config{
		gatewayURL:    values["MUSEREEL_CONFORMANCE_GATEWAY_URL"],
		runtimeTarget: values["MUSEREEL_CONFORMANCE_RUNTIME_TARGET"],
		mtls: sdk.MTLSConfig{
			CertFile:   values["MUSEREEL_CONFORMANCE_MTLS_CERT_FILE"],
			KeyFile:    values["MUSEREEL_CONFORMANCE_MTLS_KEY_FILE"],
			CAFile:     values["MUSEREEL_CONFORMANCE_MTLS_CA_FILE"],
			ServerName: strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_MTLS_SERVER_NAME")),
		},
		signingKey:    values["MUSEREEL_CONFORMANCE_SIGNING_PRIVATE_KEY_FILE"],
		signingKID:    values["MUSEREEL_CONFORMANCE_SIGNING_KID"],
		instanceID:    values["MUSEREEL_CONFORMANCE_INSTANCE_ID"],
		tenantID:      values["MUSEREEL_CONFORMANCE_TENANT_ID"],
		sessionID:     values["MUSEREEL_CONFORMANCE_SESSION_ID"],
		actor:         values["MUSEREEL_CONFORMANCE_ACTOR"],
		skuID:         skuID,
		schemaVersion: schemaVersion,
		taskRef:       values["MUSEREEL_CONFORMANCE_TASK_REF"],
		deliveryMode:  deliveryMode,
		input:         input,
		parameters:    parameters,
		eventID:       eventID,
	}, nil
}

// buildConformanceSpec 为 gateway 的每个能力串构造最小合法 input/parameters。
// schemaVersion 是外层 spec 已按 SKU 解析后的值。
func buildConformanceSpec(skuID, schemaVersion string) (json.RawMessage, json.RawMessage, error) {
	switch skuID {
	case textGenerateSKU:
		return json.RawMessage(`{"messages":[{"role":"user","content":"sdk005-conformance"}]}`),
			json.RawMessage(`{"max_output_tokens":"64"}`), nil
	case moderationGenerateSKU:
		targetSchemaVersion, err := defaultSchemaVersionForSKU(textGenerateSKU)
		if err != nil {
			return nil, nil, fmt.Errorf("解析 moderation target schema_version 失败: %w", err)
		}
		targetSpec, err := json.Marshal(struct {
			SchemaVersion string          `json:"schema_version"`
			Input         json.RawMessage `json:"input"`
			Parameters    json.RawMessage `json:"parameters"`
		}{
			SchemaVersion: targetSchemaVersion,
			Input:         json.RawMessage(`{"messages":[{"role":"user","content":"sdk005-conformance"}]}`),
			Parameters:    json.RawMessage(`{"max_output_tokens":"64"}`),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("构造 moderation target spec 失败: %w", err)
		}
		input, err := json.Marshal(struct {
			TargetSKUID string          `json:"target_sku_id"`
			TargetSpec  json.RawMessage `json:"target_spec"`
		}{
			TargetSKUID: textGenerateSKU,
			TargetSpec:  targetSpec,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("构造 moderation input 失败: %w", err)
		}
		return json.RawMessage(input), json.RawMessage(`{}`), nil
	case videoGenerateSKU:
		return json.RawMessage(`{"prompt":"sdk005-conformance"}`),
			json.RawMessage(`{"seconds":"4","resolution":"480p","quality":"q1","aspect_ratio":"16:9","audio":true}`), nil
	case imageGenerateSKU:
		return json.RawMessage(`{"prompt":"sdk005-conformance"}`),
			json.RawMessage(`{"image_count":"1","resolution":"512","quality":"q2","aspect_ratio":"1:1"}`), nil
	case lyricsGenerateSKU:
		return json.RawMessage(`{"prompt":"sdk005-conformance"}`), json.RawMessage(`{}`), nil
	case musicGenerateSKU:
		return json.RawMessage(`{"prompt":"sdk005-conformance"}`), json.RawMessage(`{}`), nil
	case speechGenerateSKU:
		return json.RawMessage(`{"text":"sdk005-conformance"}`), json.RawMessage(`{}`), nil
	default:
		return nil, nil, fmt.Errorf("MUSEREEL_CONFORMANCE_SKU_ID 必须是 gateway capability 串，未知值 %q", skuID)
	}
}

// run 执行 Gateway 与 runtime-api 两段 conformance。
func run(ctx context.Context, cfg config) error {
	tlsConfig, err := sdk.NewTLSConfig(cfg.mtls)
	if err != nil {
		return fmt.Errorf("加载 mTLS 配置失败: %w", err)
	}
	signer, publicKey, err := loadSigner(cfg.signingKey, cfg.signingKID)
	if err != nil {
		return fmt.Errorf("加载 assertion signing key 失败: %w", err)
	}
	connection, err := sdk.DialRuntime(ctx, cfg.runtimeTarget, cfg.mtls)
	if err != nil {
		return fmt.Errorf("连接 runtime target 失败: %w", err)
	}
	defer connection.Close()

	tokens := sdk.NewGRPCTokenSource(connection)
	runtimeClient := sdk.NewRuntimeClient(connection, tokens,
		sdk.WithRuntimeAssertion(signer, cfg.instanceID, cfg.tenantID, cfg.sessionID))
	gatewayClient, err := sdk.NewGatewayClient(cfg.gatewayURL, tlsConfig, tokens, signer, sdk.GatewayIdentity{
		InstanceID: cfg.instanceID,
		TenantID:   cfg.tenantID,
		SessionID:  cfg.sessionID,
		Actor:      cfg.actor,
	})
	if err != nil {
		return fmt.Errorf("构造 Gateway client 失败: %w", err)
	}

	if err := runGateway(ctx, cfg, gatewayClient); err != nil {
		return err
	}
	return runRuntime(ctx, cfg, runtimeClient, tokens, publicKey)
}

// moderationReceiptForSKU 决定本次 create 是否携带审核收据。
//
// moderation SKU 自己**就是**产出收据的那一次调用，携带收据在中枢是显式拒绝
// （compliance 侧「moderation SKU 不得携带 moderation_receipt」）。而该拒绝走的内部码
// compliance_invalid_request 不在 gateway 的冻结公共码集合里，会被折叠成
// internal_error / HTTP 500 —— 于是 suite 只能看到「内部错误」，看不出是自己多发了一个字段。
// 这里按 SKU 择一，不要改成「让中枢容忍」：中枢那条拒绝是对的
// —— 中枢自己的 e2e 给 moderation 传的也是空串（sluice
// backend/pkg/app/e2e/invocation_runtime_integration_test.go:817,843）。
//
// ⚠ 返回空串是「键在、值为空」，**不是**「不发这个键」。gateway 的 parseCreateBody
// 显式要求 moderation_receipt 这个键必须存在，缺键直接 400
// （sluice backend/service/gateway/internal/httpapi/fingerprint.go:109-111）。
// ⇒ GatewayCreateRequest.ModerationReceipt 的 json tag **不能**加 omitempty，
// 加了会让这里的空串从线上消失，七个 SKU 一起变 400。
func moderationReceiptForSKU(skuID, receipt string) string {
	if skuID == moderationGenerateSKU {
		return ""
	}
	return receipt
}

const (
	receiptLegMain   = "main"
	receiptLegCancel = "cancel"
)

type moderationInvocationResult struct {
	Kind               string `json:"kind"`
	Verdict            string `json:"verdict"`
	ModerationReceipt  string `json:"moderation_receipt"`
	ReceiptExpiresAtMS *int64 `json:"receipt_expires_at_ms"`
}

// buildModerationRequest embeds the already serialized target spec in the
// moderation input. The target spec itself must not be reconstructed for this
// call: target_spec is the exact object that the target create will submit.
func buildModerationRequest(cfg config, targetSpecBytes []byte) (sdk.GatewayCreateRequest, error) {
	if len(bytes.TrimSpace(targetSpecBytes)) == 0 || !json.Valid(targetSpecBytes) {
		return sdk.GatewayCreateRequest{}, fmt.Errorf("目标 spec bytes 不是合法 JSON")
	}
	moderationSchemaVersion, err := defaultSchemaVersionForSKU(moderationGenerateSKU)
	if err != nil {
		return sdk.GatewayCreateRequest{}, fmt.Errorf("解析 moderation schema_version 失败: %w", err)
	}
	moderationInput, err := json.Marshal(struct {
		TargetSKUID string          `json:"target_sku_id"`
		TargetSpec  json.RawMessage `json:"target_spec"`
	}{
		TargetSKUID: cfg.skuID,
		TargetSpec:  json.RawMessage(targetSpecBytes),
	})
	if err != nil {
		return sdk.GatewayCreateRequest{}, fmt.Errorf("构造 moderation target input 失败: %w", err)
	}
	return sdk.GatewayCreateRequest{
		SKU:     moderationGenerateSKU,
		TaskRef: cfg.taskRef,
		Spec: sdk.GatewayInvocationSpec{
			SchemaVersion: moderationSchemaVersion,
			Input:         json.RawMessage(moderationInput),
			Parameters:    json.RawMessage(`{}`),
		},
		// F5: moderation is the receipt-producing invocation and must carry
		// the empty receipt key, not an omitted key or an external token.
		ModerationReceipt: "",
	}, nil
}

func mintModerationReceipt(ctx context.Context, cfg config, client *sdk.GatewayClient, targetSpecBytes []byte, leg string) (string, error) {
	if leg != receiptLegMain && leg != receiptLegCancel {
		return "", fmt.Errorf("收据铸造腿无效: %q", leg)
	}
	moderationRequest, err := buildModerationRequest(cfg, targetSpecBytes)
	if err != nil {
		return "", receiptMintInvocationFailure(cfg.skuID, err)
	}
	response, err := createForMode(ctx, client, moderationRequest, conformanceKey("receipt-"+leg), cfg.deliveryMode)
	if err != nil {
		return "", receiptMintInvocationFailure(cfg.skuID, err)
	}
	if response.AlreadyExists {
		return "", receiptMintInvocationFailure(cfg.skuID, fmt.Errorf("moderation create 意外暴露 AlreadyExists"))
	}
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		return "", receiptMintInvocationFailure(cfg.skuID,
			fmt.Errorf("moderation create status=%d，不在 202/200 封闭集", response.StatusCode))
	}

	invocationID, err := invocationIDFromCreate(ctx, response)
	if err != nil {
		return "", receiptMintInvocationFailure(cfg.skuID, err)
	}
	snapshot, err := pollToTerminal(ctx, client, invocationID)
	if err != nil {
		return "", receiptMintInvocationFailure(cfg.skuID, err)
	}
	if err := assertSuccessfulGatewayTerminal(snapshot); err != nil {
		return "", receiptMintInvocationFailure(cfg.skuID, err)
	}

	moderationResult, err := parseModerationInvocationResult(snapshot.Result)
	if err != nil {
		return "", receiptMintInvocationFailure(cfg.skuID, err)
	}
	return receiptFromModerationResult(cfg.skuID, moderationResult)
}

func parseModerationInvocationResult(result json.RawMessage) (moderationInvocationResult, error) {
	if len(bytes.TrimSpace(result)) == 0 {
		return moderationInvocationResult{}, fmt.Errorf("moderation 终态 result 为空")
	}
	var moderationResult moderationInvocationResult
	if err := json.Unmarshal(result, &moderationResult); err != nil {
		return moderationInvocationResult{}, fmt.Errorf("moderation 终态 result 无效: %w", err)
	}
	if moderationResult.Kind != "moderation" {
		return moderationInvocationResult{}, fmt.Errorf("moderation 终态 result.kind=%q，不是 moderation", moderationResult.Kind)
	}
	return moderationResult, nil
}

func receiptFromModerationResult(skuID string, moderationResult moderationInvocationResult) (string, error) {
	if moderationResult.Verdict != "pass" {
		return "", fmt.Errorf(
			"RECEIPT_CHAIN=mint_failed sku=%s reason=moderation_verdict_not_pass verdict=%q",
			skuID, moderationResult.Verdict)
	}
	if strings.TrimSpace(moderationResult.ModerationReceipt) == "" {
		return "", fmt.Errorf(
			"RECEIPT_CHAIN=mint_failed sku=%s reason=moderation_pass_receipt_empty",
			skuID)
	}
	return moderationResult.ModerationReceipt, nil
}

func receiptMintInvocationFailure(skuID string, err error) error {
	if gatewayErrorHasCode(err, sdk.GatewayInvalidInvocationRequest) {
		return fmt.Errorf(
			"RECEIPT_CHAIN=blocked sku=%s reason=hub_target_shape_unsupported: 中枢当前只能审核 text 形与 video 形目标: %w",
			skuID, err)
	}
	return fmt.Errorf(
		"RECEIPT_CHAIN=mint_failed sku=%s reason=moderation_invocation_failed: %w",
		skuID, err)
}

func gatewayErrorHasCode(err error, code string) bool {
	var gatewayErr *sdk.GatewayError
	return errors.As(err, &gatewayErr) && gatewayErr != nil && gatewayErr.Code == code
}

// The receipt markers are the conformance evidence for the compliance leg.
// Just like artifactLegMarker, contradictory SKU classification is an error:
// a marker must never testify for a leg that did not actually run.
func receiptChainSkippedMarker(skuID, receipt string) (string, error) {
	if skuID != moderationGenerateSKU {
		return "", fmt.Errorf("收据分类自相矛盾: 非 moderation SKU=%s 不得打 skipped", skuID)
	}
	if receipt != "" {
		return "", fmt.Errorf("收据分类自相矛盾: moderation SKU=%s 不得携带收据 %q", skuID, receipt)
	}
	return fmt.Sprintf("RECEIPT_CHAIN=skipped sku=%s", skuID), nil
}

func receiptChainMintedMarker(skuID, leg, taskRef, receipt string) (string, error) {
	if err := validateReceiptLeg(leg); err != nil {
		return "", err
	}
	if skuID == moderationGenerateSKU {
		return "", fmt.Errorf("收据分类自相矛盾: moderation SKU=%s 不得铸造收据", skuID)
	}
	if strings.TrimSpace(receipt) == "" {
		return "", fmt.Errorf("收据分类自相矛盾: 非 moderation SKU=%s 没有已铸造收据", skuID)
	}
	return fmt.Sprintf("RECEIPT_CHAIN=minted sku=%s leg=%s task_ref=%s", skuID, leg, taskRef), nil
}

func receiptChainPresentedMarker(skuID, leg, receipt string) (string, error) {
	if err := validateReceiptLeg(leg); err != nil {
		return "", err
	}
	if skuID == moderationGenerateSKU {
		return "", fmt.Errorf("收据分类自相矛盾: moderation SKU=%s 不得打 presented", skuID)
	}
	if strings.TrimSpace(receipt) == "" {
		return "", fmt.Errorf("收据分类自相矛盾: 非 moderation SKU=%s 没有可呈递收据", skuID)
	}
	return fmt.Sprintf("RECEIPT_CHAIN=presented sku=%s leg=%s", skuID, leg), nil
}

func validateReceiptLeg(leg string) error {
	switch leg {
	case receiptLegMain, receiptLegCancel:
		return nil
	default:
		return fmt.Errorf("收据腿无效: %q", leg)
	}
}

func targetCreateFailure(skuID, operation string, err error) error {
	var gatewayErr *sdk.GatewayError
	if errors.As(err, &gatewayErr) && gatewayErr != nil && gatewayErr.Code == sdk.GatewayModerationInvalidRequest {
		return fmt.Errorf(
			"gateway %s 失败: 收据链被中枢拒绝 sku=%s code=%s details=%s: %w",
			operation, skuID, gatewayErr.Code, gatewayErrorDetailsJSON(gatewayErr.Details), err)
	}
	return fmt.Errorf("gateway %s 失败 sku=%s: %w", operation, skuID, err)
}

func gatewayErrorDetailsJSON(details map[string]any) string {
	if details == nil {
		return "null"
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Sprintf("%v", details)
	}
	return string(encoded)
}

func replayProbeMarker(ctx context.Context, client *sdk.GatewayClient, request sdk.GatewayCreateRequest, key string, mode sdk.GatewayDeliveryMode, skuID string) (string, error) {
	response, err := createForMode(ctx, client, request, key, mode)
	if err != nil {
		return receiptReplayResultMarker(skuID, sdk.GatewayCreateResponse{}, err)
	}
	if response.Stream != nil {
		defer response.Stream.Close()
	}
	return receiptReplayResultMarker(skuID, response, nil)
}

func receiptReplayResultMarker(skuID string, response sdk.GatewayCreateResponse, err error) (string, error) {
	if err != nil {
		if gatewayErrorHasCode(err, sdk.GatewayModerationInvalidRequest) || gatewayErrorHasCode(err, sdk.GatewayComplianceRejected) {
			return fmt.Sprintf("RECEIPT_CHAIN=replay_rejected sku=%s", skuID), nil
		}
		return "", fmt.Errorf("gateway receipt replay probe 失败: %w", err)
	}
	if response.AlreadyExists || (response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK) {
		return "", fmt.Errorf(
			"gateway receipt replay probe status=%d already_exists=%t，不在 202/200 封闭集",
			response.StatusCode, response.AlreadyExists)
	}
	return fmt.Sprintf("RECEIPT_CHAIN=replay_accepted sku=%s", skuID), nil
}

type conformanceArtifactRef struct {
	artifactID   string
	downloadPath string
}

// artifactRefsFromResult 只接受终态 snapshot.result 中由服务端签发的产物引用。
// 结果形状按 SKU 收窄，避免把 moderation_receipt 等非产物 ID 误当成 artifact。
func artifactRefsFromResult(skuID, invocationID string, result json.RawMessage) ([]conformanceArtifactRef, error) {
	if strings.TrimSpace(invocationID) == "" {
		return nil, fmt.Errorf("invocation id 为空")
	}
	resultObject, err := decodeArtifactResultObject(result)
	if err != nil {
		return nil, err
	}

	_, hasArtifact := resultObject["artifact"]
	_, hasArtifacts := resultObject["artifacts"]
	switch skuID {
	case imageGenerateSKU:
		if hasArtifact {
			return nil, fmt.Errorf("image result 不得包含 artifact")
		}
		if !hasArtifacts {
			return nil, fmt.Errorf("image result 缺少 artifacts")
		}

		var rawArtifacts []json.RawMessage
		if err := json.Unmarshal(resultObject["artifacts"], &rawArtifacts); err != nil || rawArtifacts == nil {
			if err == nil {
				err = fmt.Errorf("值不是 JSON array")
			}
			return nil, fmt.Errorf("image result.artifacts 无效: %w", err)
		}
		var deliveredImageCount string
		if rawCount, ok := resultObject["delivered_image_count"]; !ok {
			return nil, fmt.Errorf("image result 缺少 delivered_image_count")
		} else if err := json.Unmarshal(rawCount, &deliveredImageCount); err != nil {
			return nil, fmt.Errorf("image result.delivered_image_count 必须是 JSON string: %w", err)
		}
		count, err := strconv.Atoi(deliveredImageCount)
		if err != nil || count < 0 {
			return nil, fmt.Errorf("image result.delivered_image_count 不是非负整数 string: %q", deliveredImageCount)
		}
		if len(rawArtifacts) != count {
			return nil, fmt.Errorf("image result.artifacts 长度=%d，不等于 delivered_image_count=%d", len(rawArtifacts), count)
		}

		refs := make([]conformanceArtifactRef, 0, len(rawArtifacts))
		for index, rawArtifact := range rawArtifacts {
			ref, err := parseConformanceArtifactRef(rawArtifact, invocationID)
			if err != nil {
				return nil, fmt.Errorf("image result.artifacts[%d] 无效: %w", index, err)
			}
			refs = append(refs, ref)
		}
		return refs, nil

	case videoGenerateSKU, musicGenerateSKU, speechGenerateSKU:
		if hasArtifacts {
			return nil, fmt.Errorf("%s result 不得包含 artifacts", skuID)
		}
		if !hasArtifact {
			return nil, fmt.Errorf("%s result 缺少 artifact", skuID)
		}
		ref, err := parseConformanceArtifactRef(resultObject["artifact"], invocationID)
		if err != nil {
			return nil, fmt.Errorf("%s result.artifact 无效: %w", skuID, err)
		}
		return []conformanceArtifactRef{ref}, nil

	case textGenerateSKU, lyricsGenerateSKU, moderationGenerateSKU:
		if hasArtifact || hasArtifacts {
			return nil, fmt.Errorf("%s result 不得包含 artifact 或 artifacts", skuID)
		}
		return nil, nil

	default:
		return nil, fmt.Errorf("未知 SKU %q，无法解析 artifact result", skuID)
	}
}

func decodeArtifactResultObject(result json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(result)) == 0 {
		return nil, fmt.Errorf("终态 snapshot.result 为空")
	}
	var resultObject map[string]json.RawMessage
	if err := json.Unmarshal(result, &resultObject); err != nil {
		return nil, fmt.Errorf("终态 snapshot.result 必须是 JSON object: %w", err)
	}
	if resultObject == nil {
		return nil, fmt.Errorf("终态 snapshot.result 必须是 JSON object，不能是 null")
	}
	return resultObject, nil
}

func parseConformanceArtifactRef(rawArtifact json.RawMessage, invocationID string) (conformanceArtifactRef, error) {
	var artifactObject map[string]json.RawMessage
	if err := json.Unmarshal(rawArtifact, &artifactObject); err != nil || artifactObject == nil {
		if err == nil {
			err = fmt.Errorf("值不是 JSON object")
		}
		return conformanceArtifactRef{}, err
	}

	rawArtifactID, ok := artifactObject["artifact_id"]
	if !ok {
		return conformanceArtifactRef{}, fmt.Errorf("缺少 artifact_id")
	}
	var artifactID string
	if err := json.Unmarshal(rawArtifactID, &artifactID); err != nil || strings.TrimSpace(artifactID) == "" {
		if err == nil {
			err = fmt.Errorf("值必须是非空 JSON string")
		}
		return conformanceArtifactRef{}, fmt.Errorf("artifact_id 无效: %w", err)
	}

	rawDownloadPath, ok := artifactObject["download_path"]
	if !ok {
		return conformanceArtifactRef{}, fmt.Errorf("缺少 download_path")
	}
	var downloadPath string
	if err := json.Unmarshal(rawDownloadPath, &downloadPath); err != nil {
		return conformanceArtifactRef{}, fmt.Errorf("download_path 必须是 JSON string: %w", err)
	}
	expectedDownloadPath := "/runtime/v1/invocations/" + invocationID + "/artifacts/" + artifactID
	if downloadPath != expectedDownloadPath {
		return conformanceArtifactRef{}, fmt.Errorf("download_path=%q，不等于服务端 artifact 路径 %q", downloadPath, expectedDownloadPath)
	}

	return conformanceArtifactRef{artifactID: artifactID, downloadPath: downloadPath}, nil
}

func skuProducesArtifacts(skuID string) bool {
	switch skuID {
	case videoGenerateSKU, imageGenerateSKU, musicGenerateSKU, speechGenerateSKU:
		return true
	default:
		return false
	}
}

// artifactLegMarker 产出这条腿的机器可读标记，并在两处 SKU 分类不一致时失败。
//
// 标记是**外部矩阵判「artifact 传输腿到底跑没跑过」的唯一锚**——非产物 SKU 打 skipped、
// 产物 SKU 打 downloaded 加实际下载数。正因为它是唯一锚，两种不一致必须当场失败，
// 否则标记会替一条根本没跑的腿作证：
//
//	① skuProducesArtifacts 说不产、result 里却解出了引用。SKU 分类在本文件里写了两处
//	   （artifactRefsFromResult 的 switch 决定解析形状，这个函数决定腿跑不跑），
//	   新增产物 SKU 只改一边就会漂移，症状是打 skipped 而引用非空、下载一个都不发。
//	② 说产出、却一个引用都没有。`ARTIFACT_LEG=downloaded ... count=0` 是假标记：
//	   矩阵闭包只看 downloaded 出现过没有，会把它读成这条腿已覆盖。
func artifactLegMarker(skuID string, refCount int) (string, error) {
	produces := skuProducesArtifacts(skuID)
	if produces != (refCount > 0) {
		return "", fmt.Errorf(
			"artifact 分类自相矛盾: sku=%s skuProducesArtifacts=%t，但从 result 解析出 %d 个产物引用",
			skuID, produces, refCount)
	}
	if !produces {
		return fmt.Sprintf("ARTIFACT_LEG=skipped sku=%s", skuID), nil
	}
	return fmt.Sprintf("ARTIFACT_LEG=downloaded sku=%s count=%d", skuID, refCount), nil
}

// runGateway 覆盖 token exchange、create、GET、幂等、cancel、ETag
// 和 artifact Content-Digest 路径；所有 HTTP 交互都经过 GatewayClient。
func runGateway(ctx context.Context, cfg config, client *sdk.GatewayClient) error {
	targetSpec := sdk.GatewayInvocationSpec{
		SchemaVersion: cfg.schemaVersion,
		Input:         cfg.input,
		Parameters:    cfg.parameters,
	}
	// Serialize the target spec exactly once. The bytes are embedded in the
	// moderation input; the same targetSpec value is reused for every target
	// create, including the cancel fixture and idempotency probes.
	targetSpecBytes, err := json.Marshal(targetSpec)
	if err != nil {
		return fmt.Errorf("目标 spec 序列化失败: %w", err)
	}

	baseRequest := sdk.GatewayCreateRequest{
		SKU:     cfg.skuID,
		TaskRef: cfg.taskRef,
		Spec:    targetSpec,
	}
	mainReceipt := ""
	if cfg.skuID == moderationGenerateSKU {
		marker, markerErr := receiptChainSkippedMarker(cfg.skuID, mainReceipt)
		if markerErr != nil {
			return markerErr
		}
		fmt.Println(marker)
	} else {
		mainReceipt, err = mintModerationReceipt(ctx, cfg, client, targetSpecBytes, receiptLegMain)
		if err != nil {
			return err
		}
		marker, markerErr := receiptChainMintedMarker(cfg.skuID, receiptLegMain, cfg.taskRef, mainReceipt)
		if markerErr != nil {
			return markerErr
		}
		fmt.Println(marker)
	}
	request := baseRequest
	request.ModerationReceipt = moderationReceiptForSKU(cfg.skuID, mainReceipt)

	createKey := conformanceKey("create")
	first, err := createForMode(ctx, client, request, createKey, cfg.deliveryMode)
	if err != nil {
		return targetCreateFailure(cfg.skuID, "target create", err)
	}
	if first.AlreadyExists {
		return targetCreateFailure(cfg.skuID, "target create", fmt.Errorf("首次 create 意外暴露 AlreadyExists"))
	}
	if first.StatusCode != http.StatusAccepted && first.StatusCode != http.StatusOK {
		return targetCreateFailure(cfg.skuID, "target create",
			fmt.Errorf("create status=%d，不在 202/200 封闭集", first.StatusCode))
	}

	invocationID, err := invocationIDFromCreate(ctx, first)
	if err != nil {
		return targetCreateFailure(cfg.skuID, "target create", err)
	}
	if cfg.skuID != moderationGenerateSKU {
		marker, markerErr := receiptChainPresentedMarker(cfg.skuID, receiptLegMain, request.ModerationReceipt)
		if markerErr != nil {
			return markerErr
		}
		fmt.Println(marker)
	}
	snapshot, err := pollToTerminal(ctx, client, invocationID)
	if err != nil {
		return err
	}
	if err := assertSuccessfulGatewayTerminal(snapshot); err != nil {
		return err
	}

	if cfg.skuID != moderationGenerateSKU {
		replayProbe, probeErr := replayProbeMarker(ctx, client, request, conformanceKey("receipt-replay"), cfg.deliveryMode, cfg.skuID)
		if probeErr != nil {
			return probeErr
		}
		fmt.Println(replayProbe)
	}

	replay, err := createForMode(ctx, client, request, createKey, cfg.deliveryMode)
	if err != nil {
		return fmt.Errorf("gateway 幂等重放失败: %w", err)
	}
	if !replay.AlreadyExists || replay.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("gateway 幂等重放未暴露 303/AlreadyExists: status=%d already_exists=%t", replay.StatusCode, replay.AlreadyExists)
	}

	artifactRefs, err := artifactRefsFromResult(cfg.skuID, invocationID, snapshot.Result)
	if err != nil {
		return fmt.Errorf("从终态 snapshot.result 解析 artifact 失败: %w", err)
	}
	marker, err := artifactLegMarker(cfg.skuID, len(artifactRefs))
	if err != nil {
		return err
	}
	for _, artifactRef := range artifactRefs {
		artifact := new(bytes.Buffer)
		if err := client.DownloadArtifact(ctx, invocationID, artifactRef.artifactID, artifact); err != nil {
			return fmt.Errorf("gateway Content-Digest 校验路径失败 artifact_id=%s: %w", artifactRef.artifactID, err)
		}
	}
	// 标记只在这条腿真跑完之后才打：下载中途失败会带着错误返回，不留下「跑过了」的痕迹。
	fmt.Println(marker)

	cancelRequest := baseRequest
	cancelReceipt := ""
	if cfg.skuID != moderationGenerateSKU {
		cancelReceipt, err = mintModerationReceipt(ctx, cfg, client, targetSpecBytes, receiptLegCancel)
		if err != nil {
			return err
		}
		mintedMarker, markerErr := receiptChainMintedMarker(cfg.skuID, receiptLegCancel, cfg.taskRef, cancelReceipt)
		if markerErr != nil {
			return markerErr
		}
		fmt.Println(mintedMarker)
	}
	cancelRequest.ModerationReceipt = moderationReceiptForSKU(cfg.skuID, cancelReceipt)
	cancelKey := conformanceKey("cancel")
	cancelID, stream, err := createForCancel(ctx, client, cancelRequest, cancelKey, cfg.deliveryMode)
	if err != nil {
		return targetCreateFailure(cfg.skuID, "cancel target create", err)
	}
	if stream != nil {
		defer stream.Close()
	}
	if cfg.skuID != moderationGenerateSKU {
		presentedMarker, markerErr := receiptChainPresentedMarker(cfg.skuID, receiptLegCancel, cancelRequest.ModerationReceipt)
		if markerErr != nil {
			return markerErr
		}
		fmt.Println(presentedMarker)
	}
	cancel, cancelErr := client.Cancel(ctx, cancelID, conformanceKey("cancel-request"))
	cancelMarker, err := cancelLegMarker(cfg.skuID, cancel, cancelErr)
	if err != nil {
		return fmt.Errorf("gateway cancel 失败: %w", err)
	}
	fmt.Println(cancelMarker)
	return nil
}

// cancelLegMarker 把 cancel 腿的两种合法结果收敛为机器可读标记。
// 已终态调用返回 409/transition-conflict 时，竞态本身是合法结果；其它错误
// 仍必须失败，避免把未验证的 cancel 结果当成 conformance 通过。
func cancelLegMarker(skuID string, response sdk.GatewayCancelResponse, cancelErr error) (string, error) {
	if cancelErr != nil {
		var gatewayErr *sdk.GatewayError
		if errors.As(cancelErr, &gatewayErr) && gatewayErr != nil &&
			gatewayErr.HTTPStatus == http.StatusConflict &&
			gatewayErr.Code == sdk.GatewayInvocationTransitionConflict {
			return fmt.Sprintf("CANCEL_LEG=already_terminal sku=%s", skuID), nil
		}
		return "", cancelErr
	}
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway cancel status=%d，不在 202/200 封闭集", response.StatusCode)
	}
	if response.StatusCode == http.StatusAccepted && !response.Accepted {
		return "", fmt.Errorf("gateway cancel 202 未暴露 Accepted")
	}
	return fmt.Sprintf("CANCEL_LEG=accepted sku=%s", skuID), nil
}

// createForMode 使用 SKU 目录指定的 async 或 stream 公开方法。
func createForMode(ctx context.Context, client *sdk.GatewayClient, request sdk.GatewayCreateRequest, key string, mode sdk.GatewayDeliveryMode) (sdk.GatewayCreateResponse, error) {
	if mode == sdk.GatewayDeliveryStream {
		return client.CreateStream(ctx, request, key)
	}
	return client.CreateAsync(ctx, request, key)
}

// invocationIDFromCreate 从 async snapshot 或 stream accepted 事件取得 server ID。
func invocationIDFromCreate(ctx context.Context, response sdk.GatewayCreateResponse) (string, error) {
	if response.InvocationID != "" {
		return response.InvocationID, nil
	}
	if response.Stream == nil {
		return "", fmt.Errorf("gateway create 未返回 invocation id 或 stream")
	}
	defer response.Stream.Close()
	for {
		event, err := response.Stream.Next()
		if err != nil {
			return "", fmt.Errorf("gateway stream 读取 invocation id 失败: %w", err)
		}
		if event.InvocationID != "" {
			return event.InvocationID, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
	}
}

// pollToTerminal 使用 SDK poller 的 ETag/Retry-After 逻辑轮询到终态。
func pollToTerminal(ctx context.Context, client *sdk.GatewayClient, invocationID string) (*sdk.GatewayInvocationSnapshot, error) {
	poller, err := client.NewPoller(invocationID)
	if err != nil {
		return nil, fmt.Errorf("构造 Gateway poller 失败: %w", err)
	}
	for attempt := 0; attempt < 120; attempt++ {
		response, err := poller.Poll(ctx)
		if err != nil {
			return nil, fmt.Errorf("gateway GET 轮询失败: %w", err)
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
			return nil, fmt.Errorf("gateway GET status=%d，不在 200/304 封闭集", response.StatusCode)
		}
		if response.ETag != "" && (len(response.ETag) < 2 || response.ETag[0] != '"' || response.ETag[len(response.ETag)-1] != '"') {
			return nil, fmt.Errorf("gateway ETag 形态无效: %q", response.ETag)
		}
		if response.Snapshot != nil {
			if !isKnownGatewayState(response.Snapshot.State) {
				return nil, fmt.Errorf("gateway 返回未知 snapshot state: %q", response.Snapshot.State)
			}
			if response.Snapshot.Terminal {
				return response.Snapshot, nil
			}
		}
		if response.RetryAfter == 0 && response.NotModified {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, ctx.Err()
			}
		}
	}
	return nil, fmt.Errorf("gateway invocation 在轮询预算内未到终态")
}

// assertSuccessfulGatewayTerminal 保持 conformance 的成功终态断言为封闭集。
func assertSuccessfulGatewayTerminal(snapshot *sdk.GatewayInvocationSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("gateway invocation returned nil terminal snapshot")
	}

	switch snapshot.State {
	case sdk.GatewayStateCompleted:
		if !snapshot.Terminal {
			return fmt.Errorf("gateway invocation completed state is not terminal: state=%q", snapshot.State)
		}
		return nil
	case sdk.GatewayStateFailed, sdk.GatewayStateCancelled:
		return fmt.Errorf("gateway invocation did not complete successfully: state=%q terminal=%t reason=%s", snapshot.State, snapshot.Terminal, gatewaySnapshotFailureReason(snapshot))
	case sdk.GatewayStateReconciling, sdk.GatewayStateSettlementShortfall:
		// 这两个状态是已知的收尾中间态，不是成功终态；若它们从 poller 返回，
		// 说明服务端违反了 terminal/state 约束或 conformance 未完成，必须失败。
		return fmt.Errorf("gateway invocation ended in non-terminal reconciliation state: state=%q terminal=%t", snapshot.State, snapshot.Terminal)
	case sdk.GatewayStateAccepted, sdk.GatewayStateRunning, sdk.GatewayStateCancelPending:
		return fmt.Errorf("gateway invocation returned a non-terminal state: state=%q terminal=%t", snapshot.State, snapshot.Terminal)
	default:
		return fmt.Errorf("gateway invocation returned an unhandled terminal state: state=%q terminal=%t", snapshot.State, snapshot.Terminal)
	}
}

func gatewaySnapshotFailureReason(snapshot *sdk.GatewayInvocationSnapshot) string {
	if snapshot.Error == nil {
		return "server_error=<none>"
	}
	// GatewayError.Error 只暴露服务端稳定错误码，避免把不受信任的 Message 带入诊断。
	return snapshot.Error.Error()
}

// isKnownGatewayState 保持 Gateway 状态机断言为封闭集。
func isKnownGatewayState(state sdk.GatewayInvocationState) bool {
	switch state {
	case sdk.GatewayStateAccepted, sdk.GatewayStateRunning, sdk.GatewayStateCancelPending,
		sdk.GatewayStateReconciling, sdk.GatewayStateSettlementShortfall, sdk.GatewayStateCompleted,
		sdk.GatewayStateFailed, sdk.GatewayStateCancelled:
		return true
	default:
		return false
	}
}

// createForCancel 创建一个全新 invocation；stream 只读到首个业务事件便保留流供 cancel。
func createForCancel(ctx context.Context, client *sdk.GatewayClient, request sdk.GatewayCreateRequest, key string, mode sdk.GatewayDeliveryMode) (string, *sdk.GatewaySSEStream, error) {
	response, err := createForMode(ctx, client, request, key, mode)
	if err != nil {
		return "", nil, fmt.Errorf("gateway cancel fixture create 失败: %w", err)
	}
	if response.AlreadyExists {
		return "", nil, fmt.Errorf("gateway cancel fixture create 意外 AlreadyExists")
	}
	if response.InvocationID != "" {
		return response.InvocationID, nil, nil
	}
	if response.Stream == nil {
		return "", nil, fmt.Errorf("gateway cancel fixture 未返回 invocation id 或 stream")
	}
	event, err := response.Stream.Next()
	if err != nil {
		response.Stream.Close()
		return "", nil, fmt.Errorf("gateway cancel fixture stream 读取失败: %w", err)
	}
	if event.InvocationID == "" {
		response.Stream.Close()
		return "", nil, fmt.Errorf("gateway cancel fixture event 缺少 invocation id")
	}
	return event.InvocationID, response.Stream, nil
}

// runRuntime 覆盖 catalog string、strict nonce 和 SyncIdentity event 幂等。
func runRuntime(ctx context.Context, cfg config, client *sdk.RuntimeClient, tokens sdk.TokenSource, publicKey crypto.PublicKey) error {
	token, err := tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("ExchangeRuntimeToken 失败: %w", err)
	}
	if token.TokenType() != "Bearer" || token.ExpiresAt().IsZero() {
		return fmt.Errorf("ExchangeRuntimeToken 返回结构不变量失败")
	}

	skuCatalog, err := client.GetSkuCatalog(ctx)
	if err != nil {
		return fmt.Errorf("GetSkuCatalog 失败: %w", err)
	}
	if err := assertCatalogAmountsAreStrings(skuCatalog); err != nil {
		return fmt.Errorf("GetSkuCatalog 金额/units 类型断言失败: %w", err)
	}
	offerCatalog, err := client.GetOfferCatalog(ctx)
	if err != nil {
		return fmt.Errorf("GetOfferCatalog 失败: %w", err)
	}
	if err := assertCatalogAmountsAreStrings(offerCatalog); err != nil {
		return fmt.Errorf("GetOfferCatalog 金额/units 类型断言失败: %w", err)
	}

	balanceRequest := &runtimepb.GetBalanceRequest{Actor: cfg.actor}
	firstBalance, err := client.GetBalance(ctx, balanceRequest)
	if err != nil {
		return fmt.Errorf("GetBalance 第一次成功调用失败: %w", err)
	}
	firstBalanceAssertion := append([]byte(nil), balanceRequest.GetActorAssertion()...)
	secondBalance, err := client.GetBalance(ctx, balanceRequest)
	if err != nil {
		return fmt.Errorf("GetBalance 第二次成功调用失败: %w", err)
	}
	if err := assertStrictNonce("GetBalance", firstBalanceAssertion, balanceRequest.GetActorAssertion(), publicKey, "balance:get"); err != nil {
		return err
	}
	if firstBalance == nil || secondBalance == nil {
		return fmt.Errorf("GetBalance 返回为空")
	}

	ledgerRequest := &runtimepb.ListLedgerRequest{Actor: cfg.actor, PageSize: 0}
	firstLedger, err := client.ListLedger(ctx, ledgerRequest)
	if err != nil {
		return fmt.Errorf("ListLedger 第一次成功调用失败: %w", err)
	}
	firstLedgerAssertion := append([]byte(nil), ledgerRequest.GetActorAssertion()...)
	secondLedger, err := client.ListLedger(ctx, ledgerRequest)
	if err != nil {
		return fmt.Errorf("ListLedger 第二次成功调用失败: %w", err)
	}
	if err := assertStrictNonce("ListLedger", firstLedgerAssertion, ledgerRequest.GetActorAssertion(), publicKey, "ledger:list"); err != nil {
		return err
	}
	if firstLedger == nil || secondLedger == nil {
		return fmt.Errorf("ListLedger 返回为空")
	}

	syncRequest := &runtimepb.SyncIdentityRequest{EventId: cfg.eventID, Actor: cfg.actor, OccurredAtMs: time.Now().UnixMilli()}
	firstIdentity, err := client.SyncIdentity(ctx, syncRequest)
	if err != nil {
		return fmt.Errorf("SyncIdentity 首次调用失败: %w", err)
	}
	secondIdentity, err := client.SyncIdentity(ctx, syncRequest)
	if err != nil {
		return fmt.Errorf("SyncIdentity event_id 重放失败: %w", err)
	}
	if err := assertIdentityReplay(firstIdentity, secondIdentity, cfg.eventID); err != nil {
		return err
	}
	return nil
}

// assertIdentityReplay 比较 SyncIdentity 的业务事实字段，明确排除 request_id。
// request_id 是每次传输的追踪 ID，重放时必须重新生成而不能参与幂等事实比较。
func assertIdentityReplay(first, second *runtimepb.IdentityReply, expectedEventID string) error {
	if first == nil || second == nil {
		return fmt.Errorf("SyncIdentity 返回结构不变量失败: reply 为空")
	}
	if first.GetEventId() != second.GetEventId() {
		return fmt.Errorf("SyncIdentity event_id 重放不一致")
	}
	if first.GetIdentityId() != second.GetIdentityId() {
		return fmt.Errorf("SyncIdentity identity_id 重放不一致")
	}
	if first.GetActor() != second.GetActor() {
		return fmt.Errorf("SyncIdentity actor 重放不一致")
	}
	if first.GetIdentityStatus() != second.GetIdentityStatus() {
		return fmt.Errorf("SyncIdentity identity_status 重放不一致")
	}
	if first.GetVerificationStatus() != second.GetVerificationStatus() {
		return fmt.Errorf("SyncIdentity verification_status 重放不一致")
	}
	if first.GetStateChangedAtMs() != second.GetStateChangedAtMs() {
		return fmt.Errorf("SyncIdentity state_changed_at_ms 重放不一致")
	}
	if (first.VerificationChangedAtMs == nil) != (second.VerificationChangedAtMs == nil) {
		return fmt.Errorf("SyncIdentity verification_changed_at_ms presence 重放不一致")
	}
	if first.GetVerificationChangedAtMs() != second.GetVerificationChangedAtMs() {
		return fmt.Errorf("SyncIdentity verification_changed_at_ms 重放不一致")
	}
	if first.GetEventApplied() != second.GetEventApplied() {
		return fmt.Errorf("SyncIdentity event_applied 重放不一致")
	}
	if first.GetRequestId() == second.GetRequestId() {
		return fmt.Errorf("SyncIdentity request_id 重放必须不同")
	}
	if first.GetEventId() != expectedEventID {
		return fmt.Errorf("SyncIdentity 返回 event_id=%q，期望 %q", first.GetEventId(), expectedEventID)
	}
	return nil
}

// assertStrictNonce 验证同一读请求的 fingerprint 相同而 nonce 每次不同。
func assertStrictNonce(name string, first, second []byte, publicKey crypto.PublicKey, operation string) error {
	if len(first) == 0 || len(second) == 0 {
		return fmt.Errorf("%s strict nonce 断言缺少 assertion", name)
	}
	firstClaims, err := sdk.VerifyCompactJWS(string(first), publicKey)
	if err != nil {
		return fmt.Errorf("%s 第一次 assertion 验签失败: %w", name, err)
	}
	secondClaims, err := sdk.VerifyCompactJWS(string(second), publicKey)
	if err != nil {
		return fmt.Errorf("%s 第二次 assertion 验签失败: %w", name, err)
	}
	if firstClaims.Operation != operation || secondClaims.Operation != operation || firstClaims.RequestFingerprint != secondClaims.RequestFingerprint {
		return fmt.Errorf("%s strict nonce assertion identity 不一致", name)
	}
	if firstClaims.Nonce == "" || secondClaims.Nonce == "" || firstClaims.Nonce == secondClaims.Nonce {
		return fmt.Errorf("%s strict nonce 未产生新 nonce", name)
	}
	return nil
}

// assertCatalogAmountsAreStrings 只检查金额/units 的 JSON 类型，不检查业务数值。
func assertCatalogAmountsAreStrings(message proto.Message) error {
	encoded, err := protojson.Marshal(message)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return err
	}
	return walkCatalogValues(value, "$")
}

func walkCatalogValues(value any, path string) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			childPath := path + "." + key
			lower := strings.ToLower(key)
			if strings.Contains(lower, "units") || strings.Contains(lower, "amount") {
				if _, ok := child.(string); !ok {
					return fmt.Errorf("%s 不是 JSON string", childPath)
				}
			}
			if err := walkCatalogValues(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range value {
			if err := walkCatalogValues(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadSigner 支持 SDK 已注册的 EdDSA 与 ES256 PEM 私钥，并返回验签公钥。
func loadSigner(path, kid string) (sdk.Signer, crypto.PublicKey, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, nil, fmt.Errorf("私钥 PEM 无效")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if ecKey, ecErr := x509.ParseECPrivateKey(block.Bytes); ecErr == nil {
			key = ecKey
		} else {
			return nil, nil, err
		}
	}
	switch key := key.(type) {
	case ed25519.PrivateKey:
		signer, signerErr := sdk.NewEd25519Signer(kid, key)
		if signerErr != nil {
			return nil, nil, signerErr
		}
		return signer, key.Public(), nil
	case *ecdsa.PrivateKey:
		if key.Curve != nil && key.Curve.Params().Name != "P-256" {
			return nil, nil, fmt.Errorf("ES256 私钥不是 P-256")
		}
		signer, signerErr := sdk.NewES256Signer(kid, key)
		if signerErr != nil {
			return nil, nil, signerErr
		}
		return signer, &key.PublicKey, nil
	default:
		return nil, nil, fmt.Errorf("私钥类型未注册")
	}
}

// conformanceKey 生成满足 SDK 最小长度且同一轮可追踪的幂等键。
func conformanceKey(suffix string) string {
	return fmt.Sprintf("sdk005-conformance-%d-%s", time.Now().UnixNano(), suffix)
}
