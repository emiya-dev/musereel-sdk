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
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/emiya-dev/musereel-sdk"
	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

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
	taskRef       string
	deliveryMode  sdk.GatewayDeliveryMode
	artifactID    string
	input         json.RawMessage
	parameters    json.RawMessage
	moderation    string
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
		"MUSEREEL_CONFORMANCE_ARTIFACT_ID",
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

	input := json.RawMessage(strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_SPEC_INPUT_JSON")))
	if len(input) == 0 {
		input = json.RawMessage(`{"prompt":"sdk005-conformance"}`)
	}
	parameters := json.RawMessage(strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_SPEC_PARAMETERS_JSON")))
	if len(parameters) == 0 {
		parameters = json.RawMessage(`{"duration":"1"}`)
	}
	moderation := strings.TrimSpace(os.Getenv("MUSEREEL_CONFORMANCE_MODERATION_RECEIPT"))
	if moderation == "" {
		moderation = "e14-conformance"
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
		signingKey:   values["MUSEREEL_CONFORMANCE_SIGNING_PRIVATE_KEY_FILE"],
		signingKID:   values["MUSEREEL_CONFORMANCE_SIGNING_KID"],
		instanceID:   values["MUSEREEL_CONFORMANCE_INSTANCE_ID"],
		tenantID:     values["MUSEREEL_CONFORMANCE_TENANT_ID"],
		sessionID:    values["MUSEREEL_CONFORMANCE_SESSION_ID"],
		actor:        values["MUSEREEL_CONFORMANCE_ACTOR"],
		skuID:        values["MUSEREEL_CONFORMANCE_SKU_ID"],
		taskRef:      values["MUSEREEL_CONFORMANCE_TASK_REF"],
		deliveryMode: deliveryMode,
		artifactID:   values["MUSEREEL_CONFORMANCE_ARTIFACT_ID"],
		input:        input,
		parameters:   parameters,
		moderation:   moderation,
		eventID:      eventID,
	}, nil
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
	siteClient, err := sdk.NewGatewaySiteContextClient(cfg.gatewayURL, tlsConfig)
	if err != nil {
		return fmt.Errorf("构造 site-context client 失败: %w", err)
	}
	gatewayClient, err := sdk.NewGatewayClient(cfg.gatewayURL, tlsConfig, tokens, signer, sdk.GatewayIdentity{
		InstanceID: cfg.instanceID,
		TenantID:   cfg.tenantID,
		SessionID:  cfg.sessionID,
		Actor:      cfg.actor,
	})
	if err != nil {
		return fmt.Errorf("构造 Gateway client 失败: %w", err)
	}

	if err := runGateway(ctx, cfg, siteClient, gatewayClient); err != nil {
		return err
	}
	return runRuntime(ctx, cfg, runtimeClient, tokens, publicKey)
}

// runGateway 覆盖 site-context、token exchange、create、GET、幂等、cancel、ETag
// 和 artifact Content-Digest 路径；所有 HTTP 交互都经过 GatewayClient。
func runGateway(ctx context.Context, cfg config, siteClient *sdk.GatewaySiteContextClient, client *sdk.GatewayClient) error {
	siteContext, err := siteClient.Issue(ctx)
	if err != nil {
		return fmt.Errorf("gateway site-context 签发失败: %w", err)
	}
	if siteContext.RequestID == "" || siteContext.SiteContextToken.Reveal() == "" || siteContext.ExpiresAtMS <= 0 {
		return fmt.Errorf("gateway site-context 结构不变量失败")
	}

	request := sdk.GatewayCreateRequest{
		SKU:     cfg.skuID,
		TaskRef: cfg.taskRef,
		Spec: sdk.GatewayInvocationSpec{
			SchemaVersion: "v1",
			Input:         cfg.input,
			Parameters:    cfg.parameters,
		},
		ModerationReceipt: cfg.moderation,
	}
	createKey := conformanceKey("create")
	first, err := createForMode(ctx, client, request, createKey, cfg.deliveryMode)
	if err != nil {
		return fmt.Errorf("gateway create 失败: %w", err)
	}
	if first.AlreadyExists {
		return fmt.Errorf("gateway 首次 create 意外暴露 AlreadyExists")
	}
	if first.StatusCode != http.StatusAccepted && first.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway create status=%d，不在 202/200 封闭集", first.StatusCode)
	}

	invocationID, err := invocationIDFromCreate(ctx, first)
	if err != nil {
		return err
	}
	snapshot, err := pollToTerminal(ctx, client, invocationID)
	if err != nil {
		return err
	}
	if err := assertSuccessfulGatewayTerminal(snapshot); err != nil {
		return err
	}

	replay, err := createForMode(ctx, client, request, createKey, cfg.deliveryMode)
	if err != nil {
		return fmt.Errorf("gateway 幂等重放失败: %w", err)
	}
	if !replay.AlreadyExists || replay.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("gateway 幂等重放未暴露 303/AlreadyExists: status=%d already_exists=%t", replay.StatusCode, replay.AlreadyExists)
	}

	artifact := new(bytes.Buffer)
	if err := client.DownloadArtifact(ctx, invocationID, cfg.artifactID, artifact); err != nil {
		return fmt.Errorf("gateway Content-Digest 校验路径失败: %w", err)
	}

	cancelKey := conformanceKey("cancel")
	cancelID, stream, err := createForCancel(ctx, client, request, cancelKey, cfg.deliveryMode)
	if err != nil {
		return err
	}
	if stream != nil {
		defer stream.Close()
	}
	cancel, err := client.Cancel(ctx, cancelID, conformanceKey("cancel-request"))
	if err != nil {
		return fmt.Errorf("gateway cancel 失败: %w", err)
	}
	if cancel.StatusCode != http.StatusAccepted && cancel.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway cancel status=%d，不在 202/200 封闭集", cancel.StatusCode)
	}
	if cancel.StatusCode == http.StatusAccepted && !cancel.Accepted {
		return fmt.Errorf("gateway cancel 202 未暴露 Accepted")
	}
	return nil
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
	if !proto.Equal(firstIdentity, secondIdentity) {
		return fmt.Errorf("SyncIdentity 同 event_id 重放未返回相同 reply")
	}
	if firstIdentity == nil || firstIdentity.GetEventId() != cfg.eventID {
		return fmt.Errorf("SyncIdentity 返回结构不变量失败")
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
