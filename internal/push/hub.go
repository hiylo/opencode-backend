package push

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// Message severity levels for client notification routing.
const (
	Info     = "info"
	Warning  = "warning"
	Critical = "critical"
)

// Message is a push event sent from the server to connected clients.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// Severity routes the event to client notification channels
	// (info = silent, warning/critical = notify).
	Severity string `json:"severity,omitempty"`
}

// Hub fans out push messages to all connected WebSocket clients.
// It is safe for concurrent use.
type Hub struct {
	mu       sync.Mutex
	clients  map[*websocket.Conn]struct{}
	register chan *websocket.Conn
	unreg    chan *websocket.Conn
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{
		clients:  make(map[*websocket.Conn]struct{}),
		register: make(chan *websocket.Conn),
		unreg:    make(chan *websocket.Conn),
	}
}

// Register hands a connection to the hub's management goroutine.
func (h *Hub) Register(c *websocket.Conn) {
	h.register <- c
}

// Unregister removes a connection from the hub.
func (h *Hub) Unregister(c *websocket.Conn) {
	h.unreg <- c
}

// Run is the hub management loop; call it once in a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
		case c := <-h.unreg:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				c.Close()
			}
			h.mu.Unlock()
		}
	}
}

// Broadcast sends a message to all connected clients.
// Send failures are logged and the connection is closed and dropped.
func (h *Hub) Broadcast(msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("push: marshal message: %v", err)
		return
	}
	for c := range h.clients {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("push: write to client: %v", err)
			delete(h.clients, c)
			c.Close()
		}
	}
}
