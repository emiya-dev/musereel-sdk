package musereelsdk

import (
	"fmt"
	"strings"
)

// GatewayRoute identifies one of the four invocation routes frozen for the
// assertion fingerprint surface.
type GatewayRoute string

const (
	GatewayInvocationCreate      GatewayRoute = "invocation:create"
	GatewayInvocationGet         GatewayRoute = "invocation:get"
	GatewayInvocationGetArtifact GatewayRoute = "invocation:get_artifact"
	GatewayInvocationCancel      GatewayRoute = "invocation:cancel"
)

const gatewayInvocationPrefix = "/runtime/v1/invocations"

var runtimeMethods = map[string]struct{}{
	"ExchangeRuntimeToken":    {},
	"ResolveRegistration":     {},
	"ConfirmRegistration":     {},
	"CreateOrder":             {},
	"VerifyAndConfirmPayment": {},
	"GetOrder":                {},
	"SyncIdentity":            {},
	"SyncVerificationStatus":  {},
	"DisableIdentity":         {},
	"GetBalance":              {},
	"ListLedger":              {},
	"GetSkuCatalog":           {},
	"GetOfferCatalog":         {},
	"ListSiteBranding":        {},
}

var runtimeQueryMethods = map[string]struct{}{
	"GetOrder":         {},
	"GetBalance":       {},
	"ListLedger":       {},
	"GetSkuCatalog":    {},
	"GetOfferCatalog":  {},
	"ListSiteBranding": {},
}

var runtimeAssertionOperations = map[string]string{
	"ConfirmRegistration": "registration:confirm",
	"CreateOrder":         "order:create",
	"GetOrder":            "order:get",
	"GetBalance":          "balance:get",
	"ListLedger":          "ledger:list",
}

// CanonicalGatewayPath builds and validates a registered invocation path.
// The route-specific IDs are server-issued path segments and therefore must
// already be URL-safe; the SDK never URL-escapes them as part of fingerprint
// construction. Create accepts no IDs, get/cancel accept one invocation ID,
// and get_artifact accepts an invocation ID followed by an artifact ID.
func CanonicalGatewayPath(route GatewayRoute, ids ...string) (string, error) {
	var path string
	switch route {
	case GatewayInvocationCreate:
		for _, id := range ids {
			if id != "" {
				return "", fmt.Errorf("create invocation path does not take an id")
			}
		}
		path = gatewayInvocationPrefix
	case GatewayInvocationGet:
		if len(ids) != 1 || !validServerIssuedID(ids[0]) {
			return "", fmt.Errorf("get invocation path requires one valid invocation id")
		}
		path = gatewayInvocationPrefix + "/" + ids[0]
	case GatewayInvocationGetArtifact:
		if len(ids) != 2 || !validServerIssuedID(ids[0]) || !validServerIssuedID(ids[1]) {
			return "", fmt.Errorf("get artifact path requires valid invocation and artifact ids")
		}
		path = gatewayInvocationPrefix + "/" + ids[0] + "/artifacts/" + ids[1]
	case GatewayInvocationCancel:
		if len(ids) != 1 || !validServerIssuedID(ids[0]) {
			return "", fmt.Errorf("cancel invocation path requires one valid invocation id")
		}
		path = gatewayInvocationPrefix + "/" + ids[0]
	default:
		return "", fmt.Errorf("unregistered gateway route")
	}
	if err := ValidateCanonicalPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// CanonicalPath validates an already assembled canonical path and returns it
// unchanged. Canonicalization here is validation, not normalization: any
// spelling that would require normalization is rejected to keep fingerprints
// stable across clients.
func CanonicalPath(path string) (string, error) {
	if err := ValidateCanonicalPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// ValidateCanonicalPath accepts exactly registered gateway invocation paths
// and runtime.v1.RuntimeService full method paths.
func ValidateCanonicalPath(path string) error {
	if path == "" || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("canonical path must start with /")
	}
	if strings.ContainsAny(path, "?#") || strings.Contains(path, "//") || strings.Contains(path, "%") {
		return fmt.Errorf("canonical path contains query, escaping, or repeated slash")
	}
	segments := strings.Split(path[1:], "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("canonical path contains an invalid segment")
		}
	}
	if isRegisteredGatewayPath(path) {
		return nil
	}
	if len(segments) == 2 && segments[0] == "runtime.v1.RuntimeService" {
		if _, ok := runtimeMethods[segments[1]]; !ok {
			return fmt.Errorf("canonical runtime method is not registered")
		}
		return nil
	}
	return fmt.Errorf("canonical path is not registered")
}

func isRegisteredGatewayPath(path string) bool {
	if path == gatewayInvocationPrefix {
		return true
	}
	if !strings.HasPrefix(path, gatewayInvocationPrefix+"/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, gatewayInvocationPrefix+"/"), "/")
	if len(segments) != 1 && len(segments) != 3 {
		return false
	}
	if !validServerIssuedID(segments[0]) {
		return false
	}
	if len(segments) == 1 {
		return true
	}
	return len(segments) == 3 && segments[1] == "artifacts" && validServerIssuedID(segments[2])
}

func validServerIssuedID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("-._~", character) {
			continue
		}
		return false
	}
	return true
}

func gatewayOperationAndMethod(method, path string) (operation, canonicalMethod string, ok bool) {
	if !isRegisteredGatewayPath(path) {
		return "", "", false
	}
	method = strings.ToUpper(method)
	switch {
	case path == gatewayInvocationPrefix && method == "POST":
		return string(GatewayInvocationCreate), "POST", true
	case path == gatewayInvocationPrefix && method != "POST":
		return "", "", false
	case strings.Count(strings.TrimPrefix(path, gatewayInvocationPrefix), "/") == 1:
		return gatewayOperationForInvocationResource(method)
	case strings.Contains(path, "/artifacts/") && method == "GET":
		return string(GatewayInvocationGetArtifact), "GET", true
	default:
		return "", "", false
	}
}

func gatewayOperationForInvocationResource(method string) (operation, canonicalMethod string, ok bool) {
	switch method {
	case "GET":
		return string(GatewayInvocationGet), "GET", true
	case "DELETE":
		return string(GatewayInvocationCancel), "DELETE", true
	default:
		return "", "", false
	}
}

func runtimeOperation(path string) string {
	segments := strings.Split(path, "/")
	if len(segments) == 3 && segments[1] == "runtime.v1.RuntimeService" {
		return segments[2]
	}
	return ""
}

func idempotencyRule(method, path string) (required, forbidden bool) {
	if _, canonicalMethod, ok := gatewayOperationAndMethod(method, path); ok {
		return canonicalMethod != "GET", canonicalMethod == "GET"
	}
	if operation := runtimeOperation(path); operation != "" {
		_, forbidden := runtimeQueryMethods[operation]
		return false, forbidden
	}
	return false, false
}

func validateOperationAndMethod(method, path, operation string) error {
	if method == "" || strings.ContainsAny(method, "\r\n") {
		return fmt.Errorf("assertion method is missing or invalid")
	}
	if operation == "" || strings.ContainsAny(operation, "\r\n") {
		return fmt.Errorf("assertion operation is missing or invalid")
	}
	if isRegisteredGatewayPath(path) {
		expectedOperation, _, ok := gatewayOperationAndMethod(method, path)
		if !ok || operation != expectedOperation {
			return fmt.Errorf("assertion operation or method does not match gateway path")
		}
		return nil
	}
	if methodName := runtimeOperation(path); methodName != "" {
		expectedOperation, ok := runtimeAssertionOperations[methodName]
		if !ok || operation != expectedOperation {
			return fmt.Errorf("assertion operation does not match runtime method")
		}
	}
	return nil
}
