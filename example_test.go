package musereelsdk_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	musereelsdk "github.com/emiya-dev/musereel-sdk"
)

func ExampleNewEd25519Signer() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := musereelsdk.NewEd25519Signer("example-ed25519-kid", privateKey)
	if err != nil {
		panic(err)
	}
	_ = signer
	// Output:
}

func ExampleNewEd25519SignerFromPEM() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		panic(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	signer, err := musereelsdk.NewEd25519SignerFromPEM("example-ed25519-kid", pemBytes)
	if err != nil {
		panic(err)
	}
	_ = signer
	// Output:
}

func ExampleNewES256Signer() {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := musereelsdk.NewES256Signer("example-es256-kid", privateKey)
	if err != nil {
		panic(err)
	}
	_ = signer
	// Output:
}

func ExampleNewES256SignerFromPEM() {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		panic(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	signer, err := musereelsdk.NewES256SignerFromPEM("example-es256-kid", pemBytes)
	if err != nil {
		panic(err)
	}
	_ = signer
	// Output:
}

func ExampleCanonicalGatewayPath() {
	path, err := musereelsdk.CanonicalGatewayPath(
		musereelsdk.GatewayInvocationGetArtifact,
		"invocation-example",
		"artifact-example",
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(path)
	// Output:
	// /runtime/v1/invocations/invocation-example/artifacts/artifact-example
}

func ExampleRequestFingerprint() {
	fingerprint, err := musereelsdk.RequestFingerprint(
		"POST",
		"/runtime/v1/invocations",
		"actor@example",
		"idempotency-example",
		[]byte(`{"prompt":"hello"}`),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(fingerprint)
	// Output:
	// 3YxxQLRqVlYoGoNNxmyczn1pDG7af8dsKcxKqqF9kqU
}

func ExampleSignAssertion() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := musereelsdk.NewEd25519Signer("example-assertion-kid", privateKey)
	if err != nil {
		panic(err)
	}
	assertion, claims, err := musereelsdk.SignAssertion(signer, musereelsdk.AssertionInput{
		InstanceID:     "instance-example",
		TenantID:       "tenant-example",
		SessionID:      "session-example",
		Actor:          "actor@example",
		Operation:      string(musereelsdk.GatewayInvocationCreate),
		Method:         "POST",
		CanonicalPath:  "/runtime/v1/invocations",
		Body:           []byte(`{"sku_id":"text.generate.v1"}`),
		IdempotencyKey: "idempotency-example",
		IssuedAt:       time.Unix(1700000000, 0),
		TTL:            time.Minute,
	})
	if err != nil {
		panic(err)
	}
	_ = assertion.Compact()
	_ = claims.RequestFingerprint
	// Output:
}

func ExampleSignActorAssertion() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := musereelsdk.NewEd25519Signer("example-actor-kid", privateKey)
	if err != nil {
		panic(err)
	}
	assertion, err := musereelsdk.SignActorAssertion(signer, musereelsdk.AssertionInput{
		InstanceID:     "instance-example",
		TenantID:       "tenant-example",
		SessionID:      "session-example",
		Actor:          "actor@example",
		Operation:      string(musereelsdk.GatewayInvocationCreate),
		Method:         "POST",
		CanonicalPath:  "/runtime/v1/invocations",
		Body:           []byte(`{"sku_id":"text.generate.v1"}`),
		IdempotencyKey: "idempotency-example",
	})
	if err != nil {
		panic(err)
	}
	_ = assertion.Bytes()
	// Output:
}

func ExampleNewToken() {
	token, err := musereelsdk.NewToken(
		"example-token",
		"Bearer",
		time.Unix(1700000000, 0).Add(5*time.Minute),
	)
	if err != nil {
		panic(err)
	}
	_ = token.TokenType()
	// Output:
}

func ExampleNewCachedTokenSource() {
	now := time.Unix(1700000000, 0)
	source := musereelsdk.NewCachedTokenSource(
		func(context.Context) (musereelsdk.Token, error) {
			return musereelsdk.NewToken("example-token", "Bearer", now.Add(5*time.Minute))
		},
		musereelsdk.WithClock(func() time.Time { return now }),
	)
	token, err := source.Token(context.Background())
	if err != nil {
		panic(err)
	}
	_ = token.AccessToken()
	// Output:
}

func ExampleMTLSConfig() {
	config := musereelsdk.MTLSConfig{
		CertFile:   "/path/to/client.crt",
		KeyFile:    "/path/to/client.key",
		CAFile:     "/path/to/ca.pem",
		ServerName: "gateway.example.invalid",
	}
	_ = config
	// Output:
}

func ExampleNewRuntimeClient() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := musereelsdk.NewEd25519Signer("example-runtime-kid", privateKey)
	if err != nil {
		panic(err)
	}
	now := time.Unix(1700000000, 0)
	tokens := musereelsdk.NewCachedTokenSource(
		func(context.Context) (musereelsdk.Token, error) {
			return musereelsdk.NewToken("example-token", "Bearer", now.Add(5*time.Minute))
		},
		musereelsdk.WithClock(func() time.Time { return now }),
	)
	client := musereelsdk.NewRuntimeClient(
		nil,
		tokens,
		musereelsdk.WithRuntimeAssertion(signer, "instance-example", "tenant-example", "session-example"),
	)
	_ = client
	// Output:
}

func ExampleNewAuthenticatedClient() {
	now := time.Unix(1700000000, 0)
	tokens := musereelsdk.NewCachedTokenSource(
		func(context.Context) (musereelsdk.Token, error) {
			return musereelsdk.NewToken("example-token", "Bearer", now.Add(5*time.Minute))
		},
		musereelsdk.WithClock(func() time.Time { return now }),
	)
	client := musereelsdk.NewAuthenticatedClient(nil, tokens)
	_ = client
	// Output:
}

func ExampleGatewayCreateRequest() {
	request := musereelsdk.GatewayCreateRequest{
		SKU:     "text.generate.v1",
		TaskRef: "task-example",
		Spec: musereelsdk.GatewayInvocationSpec{
			SchemaVersion: "1",
			Input:         map[string]string{"prompt": "hello"},
			Parameters:    map[string]string{},
		},
	}
	if err := request.Validate(); err != nil {
		panic(err)
	}
	// Output:
}

func ExampleNewGatewayClient() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := musereelsdk.NewEd25519Signer("example-gateway-kid", privateKey)
	if err != nil {
		panic(err)
	}
	now := time.Unix(1700000000, 0)
	tokens := musereelsdk.NewCachedTokenSource(
		func(context.Context) (musereelsdk.Token, error) {
			return musereelsdk.NewToken("example-token", "Bearer", now.Add(5*time.Minute))
		},
		musereelsdk.WithClock(func() time.Time { return now }),
	)
	client, err := musereelsdk.NewGatewayClient(
		"https://gateway.example.invalid",
		&tls.Config{MinVersion: musereelsdk.MinimumTLSVersion},
		tokens,
		signer,
		musereelsdk.GatewayIdentity{
			InstanceID: "instance-example",
			TenantID:   "tenant-example",
			SessionID:  "session-example",
			Actor:      "actor@example",
		},
	)
	if err != nil {
		panic(err)
	}
	_ = client
	// Output:
}
