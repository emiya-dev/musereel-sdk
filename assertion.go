package musereelsdk

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/emiya-dev/musereel-sdk/jcs"
)

const (
	// ⚠ 这里曾有一个 assertionSubject = "X-Sluice-Actor" 常量，把 06 契约的
	// 「`sub` 必须等于 `X-Sluice-Actor`」读成了「等于这个字符串」——契约指的是该请求头
	// 携带的 **actor 值**。后果是 SDK 签出的每一份 assertion 都会被服务端
	// `claims.Subject != request.Actor` 拒为 actor_assertion_invalid，
	// 即除非 actor 恰好叫 "X-Sluice-Actor"，SDK 对真 sluice 一次也走不通。
	// sub 现在恒取 AssertionInput.Actor；validateClaims 也不再比对固定字面量。
	assertionAudience   = "sluice-runtime"
	defaultAssertionTTL = 60 * time.Second
	nonceBytes          = 16
)

// Signer signs a JWS signing input using one of the two algorithms registered
// for SDK-002. Implementations keep private key material private and redact
// it from all default formatting.
type Signer interface {
	Algorithm() string
	KeyID() string
	Sign(message []byte) ([]byte, error)
}

// Ed25519Signer implements the registered EdDSA algorithm.
type Ed25519Signer struct {
	kid string
	key ed25519.PrivateKey
}

// NewEd25519Signer validates and copies an Ed25519 private key.
func NewEd25519Signer(kid string, key ed25519.PrivateKey) (*Ed25519Signer, error) {
	if kid == "" {
		return nil, fmt.Errorf("Ed25519 signer kid is empty")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Ed25519 private key has invalid size")
	}
	return &Ed25519Signer{kid: kid, key: append(ed25519.PrivateKey(nil), key...)}, nil
}

// NewEd25519SignerFromPEM parses a PKCS#8 PEM private key.
func NewEd25519SignerFromPEM(kid string, pemBytes []byte) (*Ed25519Signer, error) {
	key, err := parsePEMPrivateKey(pemBytes)
	if err != nil {
		return nil, err
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PEM private key is not Ed25519")
	}
	return NewEd25519Signer(kid, edKey)
}

// Algorithm returns the registered JWS algorithm name, "EdDSA".
func (signer *Ed25519Signer) Algorithm() string { return "EdDSA" }

// KeyID returns the key identifier supplied when the signer was constructed.
// It returns an empty string for a nil receiver.
func (signer *Ed25519Signer) KeyID() string {
	if signer == nil {
		return ""
	}
	return signer.kid
}

// Sign returns an Ed25519 signature for message. It returns an error when the
// signer is nil or does not contain a correctly sized private key.
func (signer *Ed25519Signer) Sign(message []byte) ([]byte, error) {
	if signer == nil || len(signer.key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Ed25519 signer is not configured")
	}
	return ed25519.Sign(signer.key, message), nil
}

// String returns the fixed "[REDACTED_PRIVATE_KEY]" placeholder. It
// intentionally never formats the signer's private key.
func (signer *Ed25519Signer) String() string { return "[REDACTED_PRIVATE_KEY]" }

// GoString returns the fixed "[REDACTED_PRIVATE_KEY]" placeholder for %#v
// formatting. It intentionally never formats the signer's private key.
func (signer *Ed25519Signer) GoString() string { return "[REDACTED_PRIVATE_KEY]" }

// Format writes the fixed "[REDACTED_PRIVATE_KEY]" placeholder for every
// formatting verb, so formatting cannot expose the signer's private key.
func (signer *Ed25519Signer) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte("[REDACTED_PRIVATE_KEY]"))
}

// MarshalJSON encodes the fixed "[REDACTED_PRIVATE_KEY]" placeholder instead
// of the signer's private key.
func (signer *Ed25519Signer) MarshalJSON() ([]byte, error) {
	return json.Marshal("[REDACTED_PRIVATE_KEY]")
}

// ECDSAP256Signer implements ES256 with the JWS-required fixed-width R||S
// signature encoding rather than ASN.1 DER.
type ECDSAP256Signer struct {
	kid string
	key *ecdsa.PrivateKey
}

// NewES256Signer validates and copies the P-256 private key.
func NewES256Signer(kid string, key *ecdsa.PrivateKey) (*ECDSAP256Signer, error) {
	if kid == "" {
		return nil, fmt.Errorf("ES256 signer kid is empty")
	}
	if key == nil || !isP256(key.Curve) || key.D == nil {
		return nil, fmt.Errorf("ES256 signer requires a P-256 private key")
	}
	return &ECDSAP256Signer{
		kid: kid,
		key: &ecdsa.PrivateKey{PublicKey: key.PublicKey, D: new(big.Int).Set(key.D)},
	}, nil
}

// NewES256SignerFromPEM parses a PKCS#8 or SEC1 PEM private key.
func NewES256SignerFromPEM(kid string, pemBytes []byte) (*ECDSAP256Signer, error) {
	key, err := parsePEMPrivateKey(pemBytes)
	if err != nil {
		return nil, err
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PEM private key is not ECDSA")
	}
	return NewES256Signer(kid, ecdsaKey)
}

// Algorithm returns the registered JWS algorithm name, "ES256".
func (signer *ECDSAP256Signer) Algorithm() string { return "ES256" }

// KeyID returns the key identifier supplied when the signer was constructed.
// It returns an empty string for a nil receiver.
func (signer *ECDSAP256Signer) KeyID() string {
	if signer == nil {
		return ""
	}
	return signer.kid
}

// Sign returns an ES256 signature for message using SHA-256 and the JWS
// fixed-width R||S encoding. It returns an error when the signer is nil or does
// not contain a configured P-256 private key.
func (signer *ECDSAP256Signer) Sign(message []byte) ([]byte, error) {
	if signer == nil || signer.key == nil || !isP256(signer.key.Curve) {
		return nil, fmt.Errorf("ES256 signer is not configured")
	}
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, signer.key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("ES256 signing failed")
	}
	width := (signer.key.Curve.Params().BitSize + 7) / 8
	signature := make([]byte, width*2)
	r.FillBytes(signature[:width])
	s.FillBytes(signature[width:])
	return signature, nil
}

// String returns the fixed "[REDACTED_PRIVATE_KEY]" placeholder. It
// intentionally never formats the signer's private key.
func (signer *ECDSAP256Signer) String() string { return "[REDACTED_PRIVATE_KEY]" }

// GoString returns the fixed "[REDACTED_PRIVATE_KEY]" placeholder for %#v
// formatting. It intentionally never formats the signer's private key.
func (signer *ECDSAP256Signer) GoString() string { return "[REDACTED_PRIVATE_KEY]" }

// Format writes the fixed "[REDACTED_PRIVATE_KEY]" placeholder for every
// formatting verb, so formatting cannot expose the signer's private key.
func (signer *ECDSAP256Signer) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte("[REDACTED_PRIVATE_KEY]"))
}

// MarshalJSON encodes the fixed "[REDACTED_PRIVATE_KEY]" placeholder instead
// of the signer's private key.
func (signer *ECDSAP256Signer) MarshalJSON() ([]byte, error) {
	return json.Marshal("[REDACTED_PRIVATE_KEY]")
}

func parsePEMPrivateKey(pemBytes []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("private key PEM is invalid")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("private key encoding is unsupported")
}

// JWS is a compact actor assertion. Its compact value is redacted by default
// and can only be obtained through Compact for transmission.
type JWS struct {
	compact SecretString
}

func newJWS(compact string) JWS { return JWS{compact: newSecretString(compact)} }

// Compact returns the compact JWS for an explicit transport operation.
func (jws JWS) Compact() string { return jws.compact.Reveal() }

// Bytes returns a copy of the compact JWS bytes for a protobuf bytes field.
func (jws JWS) Bytes() []byte { return []byte(jws.Compact()) }

// String returns the fixed "[REDACTED]" placeholder instead of the compact
// JWS. This is intentional: logging or printing a JWS must not expose its
// assertion claims or signature.
func (jws JWS) String() string { return redactedText }

// GoString returns the fixed "[REDACTED]" placeholder for %#v formatting
// instead of the compact JWS.
func (jws JWS) GoString() string { return redactedText }

// Format writes the fixed "[REDACTED]" placeholder for every formatting verb,
// so formatting cannot expose the compact JWS.
func (jws JWS) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte(redactedText))
}

// MarshalJSON encodes the fixed "[REDACTED]" placeholder instead of the
// compact JWS, so ordinary JSON serialization cannot expose the assertion.
func (jws JWS) MarshalJSON() ([]byte, error) { return json.Marshal(redactedText) }

// AssertionInput contains token-bound identity context and the current
// operation request. InstanceID and TenantID are assertion claims supplied by
// the caller's already-bound runtime-token context; they are never put into
// the empty ExchangeRuntimeTokenRequest.
type AssertionInput struct {
	InstanceID     string
	TenantID       string
	SessionID      string
	Actor          string
	Operation      string
	Method         string
	CanonicalPath  string
	Body           []byte
	IdempotencyKey string
	IssuedAt       time.Time
	TTL            time.Duration
}

// AssertionClaims is the fixed actor assertion payload.
type AssertionClaims struct {
	Issuer             string `json:"iss"`
	Subject            string `json:"sub"`
	Audience           string `json:"aud"`
	TenantID           string `json:"tenant_id"`
	SessionID          string `json:"session_id"`
	Operation          string `json:"operation"`
	RequestFingerprint string `json:"request_fingerprint"`
	IssuedAt           int64  `json:"iat"`
	ExpiresAt          int64  `json:"exp"`
	Nonce              string `json:"nonce"`
}

// RequestFingerprint computes the frozen SHA-256 fingerprint. Empty body is
// canonicalized as {}, and the returned base64url has no padding.
func RequestFingerprint(method, canonicalPath, actor, idempotencyKey string, body []byte) (string, error) {
	path, err := CanonicalPath(canonicalPath)
	if err != nil {
		return "", err
	}
	if isRegisteredGatewayPath(path) {
		if _, _, ok := gatewayOperationAndMethod(method, path); !ok {
			return "", fmt.Errorf("method does not match gateway path")
		}
	}
	if actor == "" || strings.ContainsAny(actor, "\r\n") {
		return "", fmt.Errorf("actor is missing or invalid")
	}
	if strings.ContainsAny(idempotencyKey, "\r\n") {
		return "", fmt.Errorf("idempotency key is invalid")
	}
	if method == "" || strings.ContainsAny(method, "\r\n") {
		return "", fmt.Errorf("method is missing or invalid")
	}
	canonicalBody, err := jcs.CanonicalizeJSON(body)
	if err != nil {
		return "", fmt.Errorf("canonicalize request body: %w", err)
	}
	input := strings.ToUpper(method) + "\n" + path + "\n" + actor + "\n" + idempotencyKey + "\n" + canonicalBody
	digest := sha256.Sum256([]byte(input))
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// SignAssertion creates a compact JWS with a fresh random nonce on every
// invocation. Its maximum validity window is the frozen 60 seconds.
func SignAssertion(signer Signer, input AssertionInput) (JWS, AssertionClaims, error) {
	if signer == nil {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion signer is not configured")
	}
	if signer.KeyID() == "" {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion signer kid is empty")
	}
	if signer.Algorithm() != "EdDSA" && signer.Algorithm() != "ES256" {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion algorithm is not registered")
	}
	path, err := CanonicalPath(input.CanonicalPath)
	if err != nil {
		return JWS{}, AssertionClaims{}, err
	}
	if err := validateOperationAndMethod(input.Method, path, input.Operation); err != nil {
		return JWS{}, AssertionClaims{}, err
	}
	if required, forbidden := idempotencyRule(input.Method, path); required && input.IdempotencyKey == "" {
		return JWS{}, AssertionClaims{}, fmt.Errorf("mutation assertion requires an idempotency key")
	} else if forbidden && input.IdempotencyKey != "" {
		return JWS{}, AssertionClaims{}, fmt.Errorf("query assertion must not carry an idempotency key")
	}
	if input.InstanceID == "" || input.TenantID == "" || input.SessionID == "" || input.Actor == "" {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion identity context is incomplete")
	}
	if strings.ContainsAny(input.InstanceID+input.TenantID+input.SessionID+input.Actor, "\r\n") {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion identity context is invalid")
	}
	fingerprint, err := RequestFingerprint(input.Method, path, input.Actor, input.IdempotencyKey, input.Body)
	if err != nil {
		return JWS{}, AssertionClaims{}, err
	}
	now := input.IssuedAt
	if now.IsZero() {
		now = time.Now()
	}
	ttl := input.TTL
	if ttl == 0 {
		ttl = defaultAssertionTTL
	}
	if ttl <= 0 || ttl > defaultAssertionTTL {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion lifetime must be in (0, 60s]")
	}
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion nonce generation failed")
	}
	claims := AssertionClaims{
		Issuer:             input.InstanceID,
		Subject:            input.Actor,
		Audience:           assertionAudience,
		TenantID:           input.TenantID,
		SessionID:          input.SessionID,
		Operation:          input.Operation,
		RequestFingerprint: fingerprint,
		IssuedAt:           now.Unix(),
		ExpiresAt:          now.Add(ttl).Unix(),
		Nonce:              base64.RawURLEncoding.EncodeToString(nonce),
	}
	if claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > 60 {
		return JWS{}, AssertionClaims{}, fmt.Errorf("assertion lifetime is outside the contract window")
	}
	payload, err := canonicalJSON(claims)
	if err != nil {
		return JWS{}, AssertionClaims{}, fmt.Errorf("canonicalize assertion payload: %w", err)
	}
	header, err := canonicalJSON(struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}{Algorithm: signer.Algorithm(), KeyID: signer.KeyID()})
	if err != nil {
		return JWS{}, AssertionClaims{}, fmt.Errorf("canonicalize assertion header: %w", err)
	}
	headerPart := base64.RawURLEncoding.EncodeToString([]byte(header))
	payloadPart := base64.RawURLEncoding.EncodeToString([]byte(payload))
	signingInput := headerPart + "." + payloadPart
	signature, err := signer.Sign([]byte(signingInput))
	if err != nil {
		return JWS{}, AssertionClaims{}, err
	}
	return newJWS(signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)), claims, nil
}

// SignActorAssertion is a convenience form when callers only need the
// transport value.
func SignActorAssertion(signer Signer, input AssertionInput) (JWS, error) {
	jws, _, err := SignAssertion(signer, input)
	return jws, err
}

func canonicalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return jcs.CanonicalizeJSON(raw)
}

// VerifyCompactJWS verifies an SDK compact JWS and returns its fixed claims.
// It is intended for tests and local self-checks; server registration remains
// the source of truth for kid authorization.
func VerifyCompactJWS(compact string, publicKey crypto.PublicKey) (AssertionClaims, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return AssertionClaims{}, fmt.Errorf("compact JWS shape is invalid")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS header encoding is invalid")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS payload encoding is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS signature encoding is invalid")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.KeyID == "" {
		return AssertionClaims{}, fmt.Errorf("compact JWS header is invalid")
	}
	if _, err := jcs.CanonicalizeJSON(headerBytes); err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS header is not valid JSON")
	}
	if err := rejectUnknownJSONFields(headerBytes, map[string]struct{}{"alg": {}, "kid": {}}); err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS header is invalid")
	}
	if header.Algorithm != "EdDSA" && header.Algorithm != "ES256" {
		return AssertionClaims{}, fmt.Errorf("compact JWS algorithm is not registered")
	}
	if err := verifySignature(header.Algorithm, publicKey, []byte(parts[0]+"."+parts[1]), signature); err != nil {
		return AssertionClaims{}, err
	}
	if _, err := jcs.CanonicalizeJSON(payloadBytes); err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS payload is not valid JSON")
	}
	if err := rejectUnknownJSONFields(payloadBytes, map[string]struct{}{
		"iss": {}, "sub": {}, "aud": {}, "tenant_id": {}, "session_id": {},
		"operation": {}, "request_fingerprint": {}, "iat": {}, "exp": {}, "nonce": {},
	}); err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS claims are invalid")
	}
	var claims AssertionClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return AssertionClaims{}, fmt.Errorf("compact JWS claims are invalid")
	}
	if err := validateClaims(claims); err != nil {
		return AssertionClaims{}, err
	}
	return claims, nil
}

// VerifyJWS is the typed wrapper around VerifyCompactJWS.
func VerifyJWS(jws JWS, publicKey crypto.PublicKey) (AssertionClaims, error) {
	return VerifyCompactJWS(jws.Compact(), publicKey)
}

func verifySignature(algorithm string, publicKey crypto.PublicKey, message, signature []byte) error {
	switch algorithm {
	case "EdDSA":
		key, ok := publicKey.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
			return fmt.Errorf("EdDSA verification key or signature is invalid")
		}
		if !ed25519.Verify(key, message, signature) {
			return fmt.Errorf("EdDSA signature verification failed")
		}
		return nil
	case "ES256":
		key, ok := publicKey.(*ecdsa.PublicKey)
		if !ok || key == nil || !isP256(key.Curve) || len(signature) != 64 {
			return fmt.Errorf("ES256 verification key or signature is invalid")
		}
		digest := sha256.Sum256(message)
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		if !ecdsa.Verify(key, digest[:], r, s) {
			return fmt.Errorf("ES256 signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("JWS algorithm is not registered")
	}
}

func validateClaims(claims AssertionClaims) error {
	// sub 是 actor 值，只能校验非空——它与本次请求 actor 的一致性由服务端裁决
	// （instanceauth/assertion.go 的 claims.Subject != request.Actor），
	// SDK 侧再比对一次固定字面量正是原先那个缺陷。
	if claims.Issuer == "" || claims.Subject == "" || claims.Audience != assertionAudience ||
		claims.TenantID == "" || claims.SessionID == "" || claims.Operation == "" ||
		claims.RequestFingerprint == "" || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > 60 {
		return fmt.Errorf("compact JWS claims are outside the assertion contract")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(claims.Nonce)
	if err != nil || len(nonce) < nonceBytes || base64.RawURLEncoding.EncodeToString(nonce) != claims.Nonce {
		return fmt.Errorf("compact JWS nonce is invalid")
	}
	fingerprint, err := base64.RawURLEncoding.DecodeString(claims.RequestFingerprint)
	if err != nil || len(fingerprint) != sha256.Size || base64.RawURLEncoding.EncodeToString(fingerprint) != claims.RequestFingerprint {
		return fmt.Errorf("compact JWS request fingerprint is invalid")
	}
	return nil
}

func rejectUnknownJSONFields(raw []byte, allowed map[string]struct{}) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unexpected JSON field")
		}
	}
	return nil
}

func isP256(curve elliptic.Curve) bool {
	if curve == nil || curve.Params() == nil {
		return false
	}
	return curve.Params().Name == elliptic.P256().Params().Name && curve.Params().BitSize == 256
}
