package wire

import (
	"bytes"
	"testing"
)

func TestExchangeRuntimeTokenRequestGoldenEmpty(t *testing.T) {
	codec := Codec{}
	encoded, err := codec.Marshal(&ExchangeRuntimeTokenRequest{})
	if err != nil {
		t.Fatalf("Marshal(empty request): %v", err)
	}
	if len(encoded) != 0 {
		t.Fatalf("empty request bytes = %x, want zero bytes", encoded)
	}
	var decoded ExchangeRuntimeTokenRequest
	if err := codec.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal(empty request): %v", err)
	}
	if err := codec.Unmarshal([]byte{0x30, 0x01}, &decoded); err != nil {
		t.Fatalf("Unmarshal(unknown field): %v", err)
	}
}

func TestExchangeRuntimeTokenReplyGoldenRoundTrip(t *testing.T) {
	codec := Codec{}
	wantBytes := []byte{
		0x0a, 0x03, 'r', 'e', 'q',
		0x12, 0x06, 's', 'e', 'c', 'r', 'e', 't',
		0x1a, 0x06, 'B', 'e', 'a', 'r', 'e', 'r',
		0x20, 0xac, 0x02,
		0x28, 0x7b,
	}
	var got ExchangeRuntimeTokenReply
	if err := codec.Unmarshal(wantBytes, &got); err != nil {
		t.Fatalf("Unmarshal(golden): %v", err)
	}
	want := ExchangeRuntimeTokenReply{
		RequestID:        "req",
		AccessToken:      "secret",
		TokenType:        "Bearer",
		ExpiresInSeconds: 300,
		ExpiresAtMS:      123,
	}
	if got != want {
		t.Fatalf("decoded = %#v, want %#v", got, want)
	}
	encoded, err := codec.Marshal(&got)
	if err != nil {
		t.Fatalf("Marshal(decoded): %v", err)
	}
	if !bytes.Equal(encoded, wantBytes) {
		t.Fatalf("encoded = %x, want %x", encoded, wantBytes)
	}
}

func TestExchangeRuntimeTokenReplyUnknownFieldsAndTypeErrors(t *testing.T) {
	codec := Codec{}
	var got ExchangeRuntimeTokenReply
	withUnknown := append([]byte{0x30, 0x01, 0x3a, 0x01, 'x'}, []byte{0x0a, 0x01, 'r'}...)
	if err := codec.Unmarshal(withUnknown, &got); err != nil {
		t.Fatalf("Unmarshal(unknown fields): %v", err)
	}
	if got.RequestID != "r" {
		t.Fatalf("RequestID = %q, want %q", got.RequestID, "r")
	}
	for _, input := range [][]byte{
		{0x08, 0x01},       // field 1 is string, not varint
		{0x20, 0x01, 0x00}, // trailing malformed tag
		{0x0a, 0xff},       // truncated length-delimited value
	} {
		if err := codec.Unmarshal(input, &got); err == nil {
			t.Errorf("Unmarshal(%x) succeeded, want error", input)
		}
	}
}
