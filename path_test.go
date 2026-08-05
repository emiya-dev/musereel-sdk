package musereelsdk

import (
	"crypto/ed25519"
	"testing"
)

func TestCanonicalGatewayRoutes(t *testing.T) {
	tests := []struct {
		name          string
		route         GatewayRoute
		ids           []string
		method        string
		wantPath      string
		wantOperation string
		wantRequired  bool
		wantForbidden bool
	}{
		{
			name:          "create",
			route:         GatewayInvocationCreate,
			method:        "POST",
			wantPath:      "/runtime/v1/invocations",
			wantOperation: string(GatewayInvocationCreate),
			wantRequired:  true,
		},
		{
			name:          "get",
			route:         GatewayInvocationGet,
			ids:           []string{"inv-01"},
			method:        "GET",
			wantPath:      "/runtime/v1/invocations/inv-01",
			wantOperation: string(GatewayInvocationGet),
			wantForbidden: true,
		},
		{
			name:          "get_artifact",
			route:         GatewayInvocationGetArtifact,
			ids:           []string{"inv-01", "artifact-01"},
			method:        "GET",
			wantPath:      "/runtime/v1/invocations/inv-01/artifacts/artifact-01",
			wantOperation: string(GatewayInvocationGetArtifact),
			wantForbidden: true,
		},
		{
			name:          "cancel",
			route:         GatewayInvocationCancel,
			ids:           []string{"inv-01"},
			method:        "DELETE",
			wantPath:      "/runtime/v1/invocations/inv-01",
			wantOperation: string(GatewayInvocationCancel),
			wantRequired:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := CanonicalGatewayPath(test.route, test.ids...)
			if err != nil {
				t.Fatalf("CanonicalGatewayPath: %v", err)
			}
			if path != test.wantPath {
				t.Fatalf("path = %q, want %q", path, test.wantPath)
			}
			if err := ValidateCanonicalPath(path); err != nil {
				t.Fatalf("ValidateCanonicalPath(%q): %v", path, err)
			}
			operation, canonicalMethod, ok := gatewayOperationAndMethod(test.method, path)
			if !ok || operation != test.wantOperation || canonicalMethod != test.method {
				t.Fatalf("gatewayOperationAndMethod(%q, %q) = %q, %q, %t", test.method, path, operation, canonicalMethod, ok)
			}
			if err := validateOperationAndMethod(test.method, path, test.wantOperation); err != nil {
				t.Fatalf("validateOperationAndMethod: %v", err)
			}
			required, forbidden := idempotencyRule(test.method, path)
			if required != test.wantRequired || forbidden != test.wantForbidden {
				t.Fatalf("idempotencyRule(%q, %q) = %t, %t", test.method, path, required, forbidden)
			}
		})
	}
}

func TestGatewayFingerprintGoldens(t *testing.T) {
	tests := []struct {
		name            string
		route           GatewayRoute
		ids             []string
		method          string
		idempotency     string
		wantFingerprint string
	}{
		{
			name:            "create",
			route:           GatewayInvocationCreate,
			method:          "POST",
			idempotency:     "create-key",
			wantFingerprint: "ml7kfh8tQ3c70ZqP8bIQxDi-vDh5l58dVh6VkpwkJxY",
		},
		{
			name:            "get",
			route:           GatewayInvocationGet,
			ids:             []string{"inv-01"},
			method:          "GET",
			wantFingerprint: "XXAR6mY20TJ08yG1U-RoY4cUIbSebh6Vk5XZ75VvI_s",
		},
		{
			name:            "get_artifact",
			route:           GatewayInvocationGetArtifact,
			ids:             []string{"inv-01", "artifact-01"},
			method:          "GET",
			wantFingerprint: "BDpVz5rIBQ7q4g4MXw4GHzqjxQsU1GliudW5k7BcEXs",
		},
		{
			name:            "cancel",
			route:           GatewayInvocationCancel,
			ids:             []string{"inv-01"},
			method:          "DELETE",
			idempotency:     "cancel-key",
			wantFingerprint: "2NHGXzc9oQNlZdI2v9EfjCyr_DZ0khqm8ZwRL3SU_O8",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := CanonicalGatewayPath(test.route, test.ids...)
			if err != nil {
				t.Fatalf("CanonicalGatewayPath: %v", err)
			}
			fingerprint, err := RequestFingerprint(test.method, path, "user-01", test.idempotency, nil)
			if err != nil {
				t.Fatalf("RequestFingerprint: %v", err)
			}
			if fingerprint != test.wantFingerprint {
				t.Fatalf("fingerprint = %q, want %q", fingerprint, test.wantFingerprint)
			}
		})
	}
}

func TestGatewayRejectsLegacyPrefix(t *testing.T) {
	if _, err := CanonicalPath("/v1/invocations"); err == nil {
		t.Fatal("legacy /v1/invocations prefix was accepted")
	}
}

func TestGatewayRejectsSingularArtifactRoute(t *testing.T) {
	if _, err := CanonicalPath("/runtime/v1/invocations/inv-01/artifact/artifact-01"); err == nil {
		t.Fatal("singular /artifact route was accepted")
	}
}

func TestGatewayRejectsCancelSubroute(t *testing.T) {
	if _, err := CanonicalPath("/runtime/v1/invocations/inv-01/cancel"); err == nil {
		t.Fatal("/cancel subroute was accepted")
	}
}

func TestGatewayRejectsMissingArtifactID(t *testing.T) {
	if _, err := CanonicalGatewayPath(GatewayInvocationGetArtifact, "inv-01"); err == nil {
		t.Fatal("artifact route without artifact ID was accepted")
	}
}

func TestGatewayRejectsInvalidArtifactIDs(t *testing.T) {
	for _, ids := range [][]string{
		{"", "artifact-01"},
		{"inv-01", ""},
		{"inv/01", "artifact-01"},
		{"inv-01", "artifact/01"},
	} {
		if _, err := CanonicalGatewayPath(GatewayInvocationGetArtifact, ids...); err == nil {
			t.Errorf("artifact IDs %q were accepted", ids)
		}
	}
}

func TestGatewayRejectsGetOperationWithDeleteMethod(t *testing.T) {
	path, err := CanonicalGatewayPath(GatewayInvocationGet, "inv-01")
	if err != nil {
		t.Fatalf("CanonicalGatewayPath: %v", err)
	}
	if err := signGatewayAssertionForPath(t, "DELETE", path, string(GatewayInvocationGet), ""); err == nil {
		t.Fatal("GET operation on DELETE method was accepted")
	}
}

func TestGatewayRejectsCancelOperationWithGetMethod(t *testing.T) {
	path, err := CanonicalGatewayPath(GatewayInvocationCancel, "inv-01")
	if err != nil {
		t.Fatalf("CanonicalGatewayPath: %v", err)
	}
	if err := signGatewayAssertionForPath(t, "GET", path, string(GatewayInvocationCancel), ""); err == nil {
		t.Fatal("cancel operation on GET method was accepted")
	}
}

func TestGatewayPostRequiresIdempotencyKey(t *testing.T) {
	path, err := CanonicalGatewayPath(GatewayInvocationCreate)
	if err != nil {
		t.Fatalf("CanonicalGatewayPath: %v", err)
	}
	if err := signGatewayAssertionForPath(t, "POST", path, string(GatewayInvocationCreate), ""); err == nil {
		t.Fatal("POST gateway assertion without idempotency key was accepted")
	}
	if err := signGatewayAssertionForPath(t, "POST", path, string(GatewayInvocationCreate), "create-key"); err != nil {
		t.Fatalf("POST gateway assertion with idempotency key: %v", err)
	}
}

func TestGatewayDeleteRequiresIdempotencyKey(t *testing.T) {
	path, err := CanonicalGatewayPath(GatewayInvocationCancel, "inv-01")
	if err != nil {
		t.Fatalf("CanonicalGatewayPath: %v", err)
	}
	if err := signGatewayAssertionForPath(t, "DELETE", path, string(GatewayInvocationCancel), ""); err == nil {
		t.Fatal("DELETE gateway assertion without idempotency key was accepted")
	}
	if err := signGatewayAssertionForPath(t, "DELETE", path, string(GatewayInvocationCancel), "cancel-key"); err != nil {
		t.Fatalf("DELETE gateway assertion with idempotency key: %v", err)
	}
}

func TestGatewayGetForbidsIdempotencyKey(t *testing.T) {
	path, err := CanonicalGatewayPath(GatewayInvocationGet, "inv-01")
	if err != nil {
		t.Fatalf("CanonicalGatewayPath: %v", err)
	}
	if err := signGatewayAssertionForPath(t, "GET", path, string(GatewayInvocationGet), "get-key"); err == nil {
		t.Fatal("GET gateway assertion with idempotency key was accepted")
	}
	if err := signGatewayAssertionForPath(t, "GET", path, string(GatewayInvocationGet), ""); err != nil {
		t.Fatalf("GET gateway assertion without idempotency key: %v", err)
	}
}

func signGatewayAssertionForPath(t *testing.T, method, path, operation, idempotencyKey string) error {
	t.Helper()
	signer, err := NewEd25519Signer("kid-path", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		return err
	}
	input := assertionInputForTest(t)
	input.Method = method
	input.CanonicalPath = path
	input.Operation = operation
	input.IdempotencyKey = idempotencyKey
	_, _, err = SignAssertion(signer, input)
	return err
}
