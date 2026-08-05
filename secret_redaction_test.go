package musereelsdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Structured loggers bypass fmt: slog/zap call String() on fmt.Stringer and
// MarshalJSON on json.Marshaler directly. Format() catching every fmt verb is
// therefore not enough on its own; each redaction method must hold
// independently, so each is asserted here as its own path.
func TestSecretStringRedactsEveryPath(t *testing.T) {
	const secret = "super-secret-token-bytes"
	s := newSecretString(secret)

	paths := map[string]string{
		"String":   s.String(),
		"GoString": s.GoString(),
		"fmt %v":   fmt.Sprintf("%v", s),
		"fmt %s":   fmt.Sprintf("%s", s),
		"fmt %+v":  fmt.Sprintf("%+v", s),
		"fmt %#v":  fmt.Sprintf("%#v", s),
		"fmt %q":   fmt.Sprintf("%q", s),
	}
	marshaled, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	paths["MarshalJSON"] = string(marshaled)

	for name, got := range paths {
		if strings.Contains(got, secret) {
			t.Errorf("%s leaks the secret value: %q", name, got)
		}
		if !strings.Contains(got, redactedText) {
			t.Errorf("%s does not carry the redaction marker: %q", name, got)
		}
	}

	if s.Reveal() != secret {
		t.Errorf("Reveal must return the original value for protocol use")
	}
}
