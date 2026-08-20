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

// String returns the fixed "[REDACTED]" placeholder instead of the stored
// value.
func (s SecretString) String() string { return redactedText }

// GoString returns the fixed "[REDACTED]" placeholder for %#v formatting
// instead of the stored value.
func (s SecretString) GoString() string { return redactedText }

// Format writes the fixed "[REDACTED]" placeholder for every formatting verb,
// so formatting cannot expose the stored value.
func (s SecretString) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, redactedText)
}

// MarshalJSON encodes the fixed "[REDACTED]" placeholder instead of the
// stored value.
func (s SecretString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedText)
}

func (s SecretString) isEmpty() bool { return s.value == "" }
