package musereelsdk

import (
	"encoding/json"
	"fmt"
	"io"
)

const redactedText = "[REDACTED]"

// SecretString holds a value that must be explicitly revealed before it is
// used as a protocol string. Its default formatting and JSON encoding never
// include the value.
type SecretString struct {
	value string
}

func newSecretString(value string) SecretString { return SecretString{value: value} }

// Reveal returns the secret for an explicit protocol operation. Callers
// should avoid storing or formatting the returned string.
func (s SecretString) Reveal() string { return s.value }

func (s SecretString) String() string { return redactedText }

func (s SecretString) GoString() string { return redactedText }

func (s SecretString) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, redactedText)
}

func (s SecretString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedText)
}

func (s SecretString) isEmpty() bool { return s.value == "" }
