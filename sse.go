package musereelsdk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// GatewaySSEEvent 是一个经过校验的业务事件。Payload 保持 raw JSON，流式结果和 units
// 不会被重新类型化。
type GatewaySSEEvent struct {
	ID           string
	Event        string
	RequestID    string
	InvocationID string
	Sequence     int64
	OccurredAtMS int64
	Payload      json.RawMessage
}

// GatewaySSEStream 解析一个 text/event-stream 响应。终态事件前断线会返回
// GatewaySSEDisconnectError，绝不会转换成取消请求。
type GatewaySSEStream struct {
	body   io.ReadCloser
	reader *bufio.Reader

	mu           sync.Mutex
	dataLines    []string
	eventName    string
	eventID      string
	accepted     bool
	usageFinal   bool
	terminal     bool
	pending      bool
	lastID       int64
	idSeen       bool
	lastSequence int64
	sequenceSeen bool
	requestID    string
	invocationID string
	closed       atomic.Bool
	closeOnce    sync.Once
}

// GatewaySSEDisconnectError 表示 HTTP 流在终态事件前结束；调用方应使用 GatewayPoller/Get
// 恢复 invocation。
type GatewaySSEDisconnectError struct{}

func (GatewaySSEDisconnectError) Error() string {
	return "gateway SSE disconnected before a terminal event"
}

func (GatewaySSEDisconnectError) Unwrap() error { return io.ErrUnexpectedEOF }

func newGatewaySSEStream(body io.ReadCloser) *GatewaySSEStream {
	return &GatewaySSEStream{body: body, reader: bufio.NewReader(body)}
}

// Next 返回下一个业务事件，跳过 : keep-alive 等注释和未知 SSE 字段；终态后的正常 EOF 为 io.EOF。
func (stream *GatewaySSEStream) Next() (GatewaySSEEvent, error) {
	if stream == nil || stream.body == nil {
		return GatewaySSEEvent{}, io.ErrClosedPipe
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed.Load() {
		return GatewaySSEEvent{}, io.ErrClosedPipe
	}

	for {
		line, readErr := stream.reader.ReadString('\n')
		if len(line) > 0 {
			if !utf8.ValidString(line) {
				return GatewaySSEEvent{}, newGatewaySSEProtocolError()
			}
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				event, ok, err := stream.dispatchEvent()
				if err != nil {
					return GatewaySSEEvent{}, err
				}
				if ok {
					return event, nil
				}
			} else if err := stream.consumeLine(line); err != nil {
				return GatewaySSEEvent{}, err
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return GatewaySSEEvent{}, newGatewaySSEProtocolError()
			}
			if len(stream.dataLines) > 0 {
				event, ok, err := stream.dispatchEvent()
				if err != nil {
					return GatewaySSEEvent{}, err
				}
				if ok {
					return event, nil
				}
			}
			if stream.terminal {
				return GatewaySSEEvent{}, io.EOF
			}
			return GatewaySSEEvent{}, GatewaySSEDisconnectError{}
		}
	}
}

// Close 释放底层 HTTP 响应 body。
func (stream *GatewaySSEStream) Close() error {
	if stream == nil || stream.body == nil {
		return nil
	}
	stream.closeOnce.Do(func() {
		stream.closed.Store(true)
		_ = stream.body.Close()
	})
	return nil
}

// Terminal 报告是否已观察到终态或 pending 事件。
func (stream *GatewaySSEStream) Terminal() bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.terminal
}

// Pending 报告终态事件是否为 invocation.pending；此时调用方应切换到 GET 轮询。
func (stream *GatewaySSEStream) Pending() bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.pending
}

func (stream *GatewaySSEStream) consumeLine(line string) error {
	if strings.HasPrefix(line, ":") {
		return nil
	}
	field := line
	value := ""
	if separator := strings.IndexByte(line, ':'); separator >= 0 {
		field = line[:separator]
		value = line[separator+1:]
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
	}
	switch field {
	case "data":
		stream.dataLines = append(stream.dataLines, value)
	case "event":
		stream.eventName = value
	case "id":
		stream.eventID = value
	default:
		// retry 和未来 SSE 字段刻意忽略。
	}
	return nil
}

func (stream *GatewaySSEStream) dispatchEvent() (GatewaySSEEvent, bool, error) {
	if len(stream.dataLines) == 0 {
		stream.resetEventFields()
		return GatewaySSEEvent{}, false, nil
	}
	data := strings.Join(stream.dataLines, "\n")
	eventName := stream.eventName
	eventID := stream.eventID
	stream.resetEventFields()
	if eventName == "" || eventID == "" {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	var envelope struct {
		RequestID    *string         `json:"request_id"`
		InvocationID *string         `json:"invocation_id"`
		Sequence     *int64          `json:"sequence"`
		OccurredAtMS *int64          `json:"occurred_at_ms"`
		Payload      json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil ||
		envelope.RequestID == nil || envelope.InvocationID == nil || envelope.Sequence == nil ||
		envelope.OccurredAtMS == nil || len(envelope.Payload) == 0 {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	if *envelope.RequestID == "" || *envelope.InvocationID == "" || *envelope.Sequence <= 0 {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	var payloadObject map[string]json.RawMessage
	if json.Unmarshal(envelope.Payload, &payloadObject) != nil || payloadObject == nil {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	id, err := strconv.ParseInt(eventID, 10, 64)
	if err != nil || id < 0 || (stream.idSeen && id <= stream.lastID) {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	if stream.sequenceSeen && *envelope.Sequence <= stream.lastSequence {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	if stream.requestID != "" && stream.requestID != *envelope.RequestID {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	if stream.invocationID != "" && stream.invocationID != *envelope.InvocationID {
		return GatewaySSEEvent{}, false, newGatewaySSEProtocolError()
	}
	stream.requestID = *envelope.RequestID
	stream.invocationID = *envelope.InvocationID
	stream.lastID = id
	stream.idSeen = true
	stream.lastSequence = *envelope.Sequence
	stream.sequenceSeen = true

	if err := stream.validateEventOrder(eventName, envelope.Payload); err != nil {
		return GatewaySSEEvent{}, false, err
	}
	return GatewaySSEEvent{
		ID:           eventID,
		Event:        eventName,
		RequestID:    *envelope.RequestID,
		InvocationID: *envelope.InvocationID,
		Sequence:     *envelope.Sequence,
		OccurredAtMS: *envelope.OccurredAtMS,
		Payload:      append(json.RawMessage(nil), envelope.Payload...),
	}, true, nil
}

func (stream *GatewaySSEStream) validateEventOrder(eventName string, payload json.RawMessage) error {
	if stream.terminal {
		return newGatewaySSEProtocolError()
	}
	switch eventName {
	case "invocation.accepted":
		if stream.accepted {
			return newGatewaySSEProtocolError()
		}
		stream.accepted = true
	case "output.delta":
		if !stream.accepted || stream.usageFinal {
			return newGatewaySSEProtocolError()
		}
	case "usage.final":
		if !stream.accepted || stream.usageFinal {
			return newGatewaySSEProtocolError()
		}
		stream.usageFinal = true
	case "invocation.completed":
		if !stream.accepted || !stream.usageFinal {
			return newGatewaySSEProtocolError()
		}
		stream.terminal = true
	case "invocation.failed", "invocation.cancelled":
		if !stream.accepted {
			return newGatewaySSEProtocolError()
		}
		stream.terminal = true
	case "invocation.pending":
		if !stream.accepted || !gatewayPendingPayloadAllowed(payload) {
			return newGatewaySSEProtocolError()
		}
		stream.terminal = true
		stream.pending = true
	default:
		return newGatewaySSEProtocolError()
	}
	return nil
}

func (stream *GatewaySSEStream) resetEventFields() {
	stream.dataLines = nil
	stream.eventName = ""
	stream.eventID = ""
}

func gatewayPendingPayloadAllowed(payload json.RawMessage) bool {
	var value struct {
		State      string `json:"state"`
		Invocation struct {
			State string `json:"state"`
		} `json:"invocation"`
	}
	if json.Unmarshal(payload, &value) != nil {
		return false
	}
	state := value.State
	if state == "" {
		state = value.Invocation.State
	}
	return state == string(GatewayStateReconciling) || state == string(GatewayStateSettlementShortfall)
}

func newGatewaySSEProtocolError() error {
	return fmt.Errorf("gateway SSE event sequence is invalid")
}
