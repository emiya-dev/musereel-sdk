package musereelsdk

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func assertionInputForTest(t *testing.T) AssertionInput {
	t.Helper()
	path, err := CanonicalGatewayPath(GatewayInvocationCreate)
	if err != nil {
		t.Fatalf("CanonicalGatewayPath: %v", err)
	}
	return AssertionInput{
		InstanceID:     "instance-01",
		TenantID:       "tenant-01",
		SessionID:      "session-01",
		Actor:          "user-01",
		Operation:      string(GatewayInvocationCreate),
		Method:         "post",
		CanonicalPath:  path,
		Body:           []byte(`{"b":2,"a":1}`),
		IdempotencyKey: "idem-01",
		IssuedAt:       time.Unix(1_800_000_000, 0),
		TTL:            60 * time.Second,
	}
}

func TestAssertionEdDSAAndFingerprint(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	signer, err := NewEd25519Signer("kid-ed", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	input := assertionInputForTest(t)
	first, firstClaims, err := SignAssertion(signer, input)
	if err != nil {
		t.Fatalf("SignAssertion(first): %v", err)
	}
	second, secondClaims, err := SignAssertion(signer, input)
	if err != nil {
		t.Fatalf("SignAssertion(second): %v", err)
	}
	if first.Compact() == second.Compact() || firstClaims.Nonce == secondClaims.Nonce {
		t.Fatal("two assertions reused the same nonce or compact value")
	}
	if firstClaims.RequestFingerprint != secondClaims.RequestFingerprint {
		t.Fatal("same business request changed its fingerprint")
	}
	verified, err := VerifyJWS(first, privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatalf("VerifyJWS(EdDSA): %v", err)
	}
	if verified != firstClaims {
		t.Fatalf("verified claims = %#v, want %#v", verified, firstClaims)
	}
	if strings.Contains(first.Compact(), "=") {
		t.Fatalf("compact JWS contains padded base64: %q", first.Compact())
	}

	wantFingerprintInput := "POST\n/runtime/v1/invocations\nuser-01\nidem-01\n{\"a\":1,\"b\":2}"
	wantDigest := sha256.Sum256([]byte(wantFingerprintInput))
	wantFingerprint := "-ThXz3tvlCkG4ey8Rq6PKt5dNQJmLELLiGNM9znVKIw"
	if firstClaims.RequestFingerprint != wantFingerprint {
		t.Fatalf("fingerprint = %q, want %q", firstClaims.RequestFingerprint, wantFingerprint)
	}
	if base64.RawURLEncoding.EncodeToString(wantDigest[:]) != wantFingerprint {
		t.Fatalf("fingerprint golden is inconsistent with its frozen input: %q", wantFingerprint)
	}
}

func TestAssertionES256AndRejectUnregisteredAlgorithm(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := NewES256Signer("kid-es", privateKey)
	if err != nil {
		t.Fatalf("NewES256Signer: %v", err)
	}
	jws, claims, err := SignAssertion(signer, assertionInputForTest(t))
	if err != nil {
		t.Fatalf("SignAssertion(ES256): %v", err)
	}
	verified, err := VerifyJWS(jws, &privateKey.PublicKey)
	if err != nil {
		t.Fatalf("VerifyJWS(ES256): %v", err)
	}
	if verified != claims {
		t.Fatalf("verified claims = %#v, want %#v", verified, claims)
	}
	if len(strings.Split(jws.Compact(), ".")) != 3 {
		t.Fatalf("compact JWS has wrong number of parts: %q", jws.Compact())
	}
	algNone := "eyJhbGciOiJub25lIiwia2lkIjoia2lkLWVzIn0.eyJzdWIiOiJ4In0.AA"
	if _, err := VerifyCompactJWS(algNone, &privateKey.PublicKey); err == nil {
		t.Fatal("alg=none was accepted")
	}
}

func TestCanonicalPathValidation(t *testing.T) {
	positive := []string{
		"/runtime/v1/invocations",
		"/runtime/v1/invocations/inv-01",
		"/runtime/v1/invocations/inv-01/artifacts/art-01",
		"/runtime.v1.RuntimeService/GetBalance",
	}
	for _, path := range positive {
		if got, err := CanonicalPath(path); err != nil || got != path {
			t.Errorf("CanonicalPath(%q) = %q, %v", path, got, err)
		}
	}
	negative := []string{
		"/v1/invocations",
		"/v1//invocations",
		"/runtime/v1/invocations/./artifacts/art-01",
		"/runtime/v1/invocations/../artifacts/art-01",
		"/runtime/v1/invocations/inv%2F01",
		"/runtime/v1/invocations/inv-01?x=1",
		"/runtime/v1/invocations/inv-01/artifact",
		"/runtime/v1/invocations/inv-01/cancel",
		"/runtime/v1/invocations/inv-01/artifacts",
		"/runtime/v1/invocations/inv-01/artifacts/art-01/extra",
		"/runtime.v1.RuntimeService//GetBalance",
		"/runtime.v1.RuntimeService/Unknown",
		"/other/route",
	}
	for _, path := range negative {
		if _, err := CanonicalPath(path); err == nil {
			t.Errorf("CanonicalPath(%q) accepted, want rejection", path)
		}
	}
}

func TestQueryAssertionHasEmptyIdempotencyKeyAndFreshNonce(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signer, err := NewEd25519Signer("kid-query", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	input := assertionInputForTest(t)
	input.CanonicalPath = "/runtime.v1.RuntimeService/GetBalance"
	input.Operation = "GetBalance"
	input.Method = "POST"
	input.IdempotencyKey = ""
	first, firstClaims, err := SignAssertion(signer, input)
	if err != nil {
		t.Fatalf("SignAssertion(first query): %v", err)
	}
	second, secondClaims, err := SignAssertion(signer, input)
	if err != nil {
		t.Fatalf("SignAssertion(second query): %v", err)
	}
	if firstClaims.RequestFingerprint != secondClaims.RequestFingerprint || firstClaims.Nonce == secondClaims.Nonce || first.Compact() == second.Compact() {
		t.Fatal("query assertions did not preserve fingerprint with a fresh nonce")
	}
	input.IdempotencyKey = "must-not-be-present"
	if _, _, err := SignAssertion(signer, input); err == nil {
		t.Fatal("query assertion accepted an idempotency key")
	}
}

func TestSecretRedaction(t *testing.T) {
	secret := "access-token-secret-value"
	token, err := NewToken(secret, "Bearer", time.Unix(1_800_000_300, 0))
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	signer, err := NewEd25519Signer("kid-redaction", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	jws, _, err := SignAssertion(signer, assertionInputForTest(t))
	if err != nil {
		t.Fatalf("SignAssertion: %v", err)
	}
	privateDER, err := x509MarshalPKCS8(signer)
	if err != nil {
		t.Fatalf("private key encoding: %v", err)
	}
	values := []any{token, jws, signer}
	for _, value := range values {
		for _, format := range []string{"%v", "%s", "%+v", "%#v"} {
			formatted := fmt.Sprintf(format, value)
			if strings.Contains(formatted, secret) || strings.Contains(formatted, string(privateDER)) {
				t.Errorf("format %s leaked secret: %q", format, formatted)
			}
			if !strings.Contains(formatted, "REDACTED") {
				t.Errorf("format %s did not redact value: %q", format, formatted)
			}
		}
	}
	encoded, err := json.Marshal(struct {
		Token  Token  `json:"token"`
		JWS    JWS    `json:"jws"`
		Signer Signer `json:"signer"`
	}{token, jws, signer})
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "REDACTED_PRIVATE_KEY") == false {
		t.Fatalf("JSON redaction failed: %s", encoded)
	}
}

// x509MarshalPKCS8 is kept in the test file so the redaction test compares
// against actual private DER without adding a production key-export method.
func x509MarshalPKCS8(signer *Ed25519Signer) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(signer.key)
}
