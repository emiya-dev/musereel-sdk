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

// TestCanonicalizeJSONSortsKeysByUTF16CodeUnits pins the RFC 8785 §3.2.3 order.
//
// The implementation previously sorted with sort.Strings (UTF-8 byte order),
// which disagrees with the frozen contract on non-BMP property names. Witness:
// U+E000 is EE 80 80 in UTF-8 while U+10000 is the surrogate pair D800 DC00 in
// UTF-16, so code-unit order puts U+10000 first and byte order puts it last.
// Reverting to sort.Strings must turn this test red.
func TestCanonicalizeJSONSortsKeysByUTF16CodeUnits(t *testing.T) {
	raw := []byte("{\"\ue000\":1,\"\U00010000\":2}")

	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		t.Fatalf("CanonicalizeJSON: %v", err)
	}

	want := "{\"\U00010000\":2,\"\ue000\":1}"
	if canonical != want {
		t.Fatalf("canonical = %q, want %q (non-BMP property names sort before U+E000 by UTF-16 code unit)",
			canonical, want)
	}
}

// TestLessUTF16MatchesCodeUnitOrder anchors the comparator itself.
func TestLessUTF16MatchesCodeUnitOrder(t *testing.T) {
	cases := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "non-BMP before private use area", left: "\U00010000", right: "\ue000", want: true},
		{name: "private use area not before non-BMP", left: "\ue000", right: "\U00010000", want: false},
		{name: "ascii matches byte order", left: "a", right: "b", want: true},
		{name: "prefix sorts first", left: "ab", right: "abc", want: true},
		{name: "equal is not less", left: "abc", right: "abc", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := lessUTF16(testCase.left, testCase.right); got != testCase.want {
				t.Fatalf("lessUTF16(%q, %q) = %v, want %v", testCase.left, testCase.right, got, testCase.want)
			}
		})
	}
}
