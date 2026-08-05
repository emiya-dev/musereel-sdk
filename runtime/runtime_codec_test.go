package runtime

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestExchangeRuntimeTokenRequestGoldenEmpty(t *testing.T) {
	encoded, err := proto.Marshal(&ExchangeRuntimeTokenRequest{})
	if err != nil {
		t.Fatalf("proto.Marshal(empty request): %v", err)
	}
	if len(encoded) != 0 {
		t.Fatalf("empty request bytes = %x, want zero bytes", encoded)
	}
	var decoded ExchangeRuntimeTokenRequest
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("proto.Unmarshal(empty request): %v", err)
	}
}

func TestExchangeRuntimeTokenReplyGoldenRoundTrip(t *testing.T) {
	wantBytes := []byte{
		0x0a, 0x03, 'r', 'e', 'q',
		0x12, 0x06, 's', 'e', 'c', 'r', 'e', 't',
		0x1a, 0x06, 'B', 'e', 'a', 'r', 'e', 'r',
		0x20, 0xac, 0x02,
		0x28, 0x7b,
	}
	var got ExchangeRuntimeTokenReply
	if err := proto.Unmarshal(wantBytes, &got); err != nil {
		t.Fatalf("proto.Unmarshal(golden): %v", err)
	}
	want := ExchangeRuntimeTokenReply{
		RequestId:        "req",
		AccessToken:      "secret",
		TokenType:        "Bearer",
		ExpiresInSeconds: 300,
		ExpiresAtMs:      123,
	}
	if !proto.Equal(&got, &want) {
		t.Fatalf("decoded fields = %q/%q/%q/%d/%d, want %q/%q/%q/%d/%d",
			got.GetRequestId(), got.GetAccessToken(), got.GetTokenType(), got.GetExpiresInSeconds(), got.GetExpiresAtMs(),
			want.GetRequestId(), want.GetAccessToken(), want.GetTokenType(), want.GetExpiresInSeconds(), want.GetExpiresAtMs())
	}
	encoded, err := proto.Marshal(&got)
	if err != nil {
		t.Fatalf("proto.Marshal(decoded): %v", err)
	}
	if !bytes.Equal(encoded, wantBytes) {
		t.Fatalf("encoded = %x, want %x", encoded, wantBytes)
	}
}

func TestExchangeRuntimeTokenReplyUnknownFieldsRemainForwardCompatible(t *testing.T) {
	withUnknown := append([]byte{0x30, 0x01, 0x3a, 0x01, 'x'}, []byte{0x0a, 0x01, 'r'}...)
	var got ExchangeRuntimeTokenReply
	if err := proto.Unmarshal(withUnknown, &got); err != nil {
		t.Fatalf("proto.Unmarshal(unknown fields): %v", err)
	}
	if got.GetRequestId() != "r" {
		t.Fatalf("RequestId = %q, want %q", got.GetRequestId(), "r")
	}
}
