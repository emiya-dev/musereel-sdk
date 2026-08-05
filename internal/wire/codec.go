// Package wire contains the temporary, narrowly scoped protobuf wire codec
// used by ExchangeRuntimeToken. It intentionally exposes no protowire details
// outside the internal package. SDK-004 codegen should replace this codec or
// retain these golden-byte assertions as an equivalent wire contract.
package wire

import (
	"fmt"

	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/encoding/protowire"
)

// CodecName is the content subtype used by the hand-written transition codec.
const CodecName = "musereel-protowire"

// ExchangeRuntimeTokenRequest mirrors runtime.proto:9-10. The message is
// intentionally empty: bootstrap identity comes from mTLS, not request
// fields.
type ExchangeRuntimeTokenRequest struct{}

// ExchangeRuntimeTokenReply mirrors runtime.proto:37-43.
type ExchangeRuntimeTokenReply struct {
	RequestID        string
	AccessToken      string
	TokenType        string
	ExpiresInSeconds int64
	ExpiresAtMS      int64
}

var _ encoding.Codec = Codec{}

// Codec implements grpc's legacy v1 encoding.Codec interface. grpc-go v1.80
// still supports this interface through ForceCodec, which keeps this
// transition isolated from future generated protobuf messages.
type Codec struct{}

func (Codec) Name() string { return CodecName }

// Marshal encodes only the two ExchangeRuntimeToken messages.
func (Codec) Marshal(value any) ([]byte, error) {
	switch typed := value.(type) {
	case *ExchangeRuntimeTokenRequest:
		if typed == nil {
			return nil, fmt.Errorf("wire: nil ExchangeRuntimeTokenRequest")
		}
		return nil, nil
	case *ExchangeRuntimeTokenReply:
		if typed == nil {
			return nil, fmt.Errorf("wire: nil ExchangeRuntimeTokenReply")
		}
		return marshalExchangeRuntimeTokenReply(typed), nil
	default:
		return nil, fmt.Errorf("wire: unsupported message %T", value)
	}
}

// Unmarshal decodes only the two ExchangeRuntimeToken messages. Unknown
// fields are skipped according to protobuf forward-compatibility semantics.
func (Codec) Unmarshal(data []byte, value any) error {
	switch typed := value.(type) {
	case *ExchangeRuntimeTokenRequest:
		if typed == nil {
			return fmt.Errorf("wire: nil ExchangeRuntimeTokenRequest")
		}
		return consumeExchangeRuntimeTokenRequest(data)
	case *ExchangeRuntimeTokenReply:
		if typed == nil {
			return fmt.Errorf("wire: nil ExchangeRuntimeTokenReply")
		}
		*typed = ExchangeRuntimeTokenReply{}
		return consumeExchangeRuntimeTokenReply(data, typed)
	default:
		return fmt.Errorf("wire: unsupported message %T", value)
	}
}

func marshalExchangeRuntimeTokenReply(reply *ExchangeRuntimeTokenReply) []byte {
	var data []byte
	// Field numbers are frozen by contract-input/runtime.proto:37-43.
	if reply.RequestID != "" {
		data = protowire.AppendTag(data, 1, protowire.BytesType)
		data = protowire.AppendString(data, reply.RequestID)
	}
	if reply.AccessToken != "" {
		data = protowire.AppendTag(data, 2, protowire.BytesType)
		data = protowire.AppendString(data, reply.AccessToken)
	}
	if reply.TokenType != "" {
		data = protowire.AppendTag(data, 3, protowire.BytesType)
		data = protowire.AppendString(data, reply.TokenType)
	}
	if reply.ExpiresInSeconds != 0 {
		data = protowire.AppendTag(data, 4, protowire.VarintType)
		data = protowire.AppendVarint(data, uint64(reply.ExpiresInSeconds))
	}
	if reply.ExpiresAtMS != 0 {
		data = protowire.AppendTag(data, 5, protowire.VarintType)
		data = protowire.AppendVarint(data, uint64(reply.ExpiresAtMS))
	}
	return data
}

func consumeExchangeRuntimeTokenRequest(data []byte) error {
	for len(data) > 0 {
		number, kind, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return wireParseError(tagLength)
		}
		valueLength := protowire.ConsumeFieldValue(number, kind, data[tagLength:])
		if valueLength < 0 {
			return wireParseError(valueLength)
		}
		data = data[tagLength+valueLength:]
	}
	return nil
}

func consumeExchangeRuntimeTokenReply(data []byte, reply *ExchangeRuntimeTokenReply) error {
	for len(data) > 0 {
		number, kind, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return wireParseError(tagLength)
		}
		field := data[tagLength:]
		var valueLength int
		switch number {
		case 1, 2, 3:
			if kind != protowire.BytesType {
				return fmt.Errorf("wire: field %d has wire type %d, want length-delimited", number, kind)
			}
			value, consumed := protowire.ConsumeString(field)
			if consumed < 0 {
				return wireParseError(consumed)
			}
			valueLength = consumed
			switch number {
			case 1:
				reply.RequestID = value
			case 2:
				reply.AccessToken = value
			case 3:
				reply.TokenType = value
			}
		case 4, 5:
			if kind != protowire.VarintType {
				return fmt.Errorf("wire: field %d has wire type %d, want varint", number, kind)
			}
			value, consumed := protowire.ConsumeVarint(field)
			if consumed < 0 {
				return wireParseError(consumed)
			}
			valueLength = consumed
			if number == 4 {
				reply.ExpiresInSeconds = int64(value)
			} else {
				reply.ExpiresAtMS = int64(value)
			}
		default:
			valueLength = protowire.ConsumeFieldValue(number, kind, field)
			if valueLength < 0 {
				return wireParseError(valueLength)
			}
		}
		data = data[tagLength+valueLength:]
	}
	return nil
}

func wireParseError(code int) error {
	return fmt.Errorf("wire: malformed protobuf: %w", protowire.ParseError(code))
}
