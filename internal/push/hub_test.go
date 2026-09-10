package push

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestConn dials a loopback WS server backed by the hub.
func newTestConn(t *testing.T, hub *Hub) *websocket.Conn {
	t.Helper()
	server := newHubServer(t, hub)

	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial("ws://"+server+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestHubBroadcast(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	conn := newTestConn(t, hub)

	// Consume confirmation: upon registration the server sends "subscribed".
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read subscribed: %v", err)
	}
	var first Message
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if first.Type != "subscribed" {
		t.Fatalf("expected subscribed, got %s", first.Type)
	}

	hub.Broadcast(Message{Type: "test.event"})
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read broadcast: %v", err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "test.event" {
		t.Fatalf("got %s want test.event", got.Type)
	}
}

func TestHubBroadcastNoClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	// Broadcasting with zero clients must not panic.
	hub.Broadcast(Message{Type: "noone"})
}