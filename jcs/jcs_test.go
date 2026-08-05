package jcs

import (
	"testing"
)

func TestCanonicalizeJSONReferenceVectors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: "{}"},
		{name: "object ordering", raw: ` { "b": 2, "a": 1 } `, want: `{"a":1,"b":2}`},
		{name: "nested", raw: `{"z":[true,null,"x"],"a":{"n":-2}}`, want: `{"a":{"n":-2},"z":[true,null,"x"]}`},
		{name: "string escaping", raw: `{"v":"<>&\u2028\u0001"}`, want: "{\"v\":\"<>&\u2028\\u0001\"}"},
		{name: "surrogate pair", raw: `{"v":"\ud834\udd1e"}`, want: "{\"v\":\"𝄞\"}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CanonicalizeJSON([]byte(test.raw))
			if err != nil {
				t.Fatalf("CanonicalizeJSON: %v", err)
			}
			if got != test.want {
				t.Fatalf("canonical = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCanonicalizeJSONRejectsReferenceNegativeSet(t *testing.T) {
	invalidUTF8 := []byte{'{', '"', 'v', '"', ':', '"', 0xff, '"', '}'}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "duplicate key", raw: []byte(`{"a":1,"a":2}`)},
		{name: "float", raw: []byte(`{"a":1.0}`)},
		{name: "exponent", raw: []byte(`{"a":1e3}`)},
		{name: "invalid utf8", raw: invalidUTF8},
		{name: "unpaired high surrogate", raw: []byte(`{"a":"\ud834"}`)},
		{name: "unpaired low surrogate", raw: []byte(`{"a":"\udd1e"}`)},
		{name: "trailing value", raw: []byte(`{"a":1} {"b":2}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := CanonicalizeJSON(test.raw); err == nil {
				t.Fatalf("canonicalized as %q, want rejection", got)
			}
		})
	}
}
