package musereelsdk

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func gatewaySSEFrameForTest(id, event string, sequence int64) string {
	return strings.Join([]string{
		"id: " + id,
		"event: " + event,
		fmt.Sprintf(`data: {"request_id":"req-01","invocation_id":"inv-01","sequence":%d,"occurred_at_ms":1800000000000,"payload":{}}`, sequence),
		"",
	}, "\n") + "\n"
}

func TestGatewaySSETerminalEOFAndDisconnect(t *testing.T) {
	terminalStream := newGatewaySSEStream(io.NopCloser(strings.NewReader(strings.Join([]string{
		gatewaySSEFrameForTest("1", "invocation.accepted", 1),
		gatewaySSEFrameForTest("2", "usage.final", 2),
		gatewaySSEFrameForTest("3", "invocation.completed", 3),
	}, ""))))
	defer terminalStream.Close()
	for _, wantEvent := range []string{"invocation.accepted", "usage.final", "invocation.completed"} {
		event, err := terminalStream.Next()
		if err != nil || event.Event != wantEvent {
			t.Fatalf("Next = %#v, %v; want %s", event, err, wantEvent)
		}
	}

	_, err := terminalStream.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("terminal stream error = %v, want io.EOF", err)
	}
	var disconnect GatewaySSEDisconnectError
	if errors.As(err, &disconnect) {
		t.Fatalf("terminal stream error = %T %v, must not be GatewaySSEDisconnectError", err, err)
	}

	disconnectedStream := newGatewaySSEStream(io.NopCloser(strings.NewReader(
		gatewaySSEFrameForTest("1", "invocation.accepted", 1),
	)))
	defer disconnectedStream.Close()
	if _, err := disconnectedStream.Next(); err != nil {
		t.Fatalf("accepted event: %v", err)
	}

	_, err = disconnectedStream.Next()
	if !errors.As(err, &disconnect) {
		t.Fatalf("disconnected stream error = %T %v, want GatewaySSEDisconnectError", err, err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("disconnected stream error = %v, want io.ErrUnexpectedEOF", err)
	}
}
