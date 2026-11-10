package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/dsiebel/mqttree/internal/broker"
)

const (
	hubInterval  = 100 * time.Millisecond
	maxBatch     = 500
	clientBuffer = 32
)

type frame struct {
	Events []broker.Event `json:"events"`
}

// Hub fans broker events out to websocket clients in 100ms batches, which keeps
// a burst from turning into thousands of tiny frames.
type Hub struct {
	in      chan broker.Event
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

// NewHub creates the hub; call Run to start delivery.
func NewHub() *Hub {
	return &Hub{
		in:      make(chan broker.Event, 4096),
		clients: map[chan []byte]struct{}{},
	}
}

// Publish hands an event to the hub without blocking the broker.
func (h *Hub) Publish(event broker.Event) {
	select {
	case h.in <- event:
	default:
		slog.Warn("dropping event: hub queue full", "type", event.Type)
	}
}

// Run batches and delivers events until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(hubInterval)
	defer ticker.Stop()
	pending := make([]broker.Event, 0, maxBatch)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		body, err := json.Marshal(frame{Events: pending})
		if err != nil {
			slog.Error("marshal event batch", "error", err)
			pending = pending[:0]
			return
		}
		h.broadcast(body)
		pending = pending[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case event := <-h.in:
			pending = append(pending, event)
			if len(pending) < maxBatch {
				continue
			}
			flush()
		case <-ticker.C:
			flush()
		}
	}
}

func (h *Hub) broadcast(body []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client <- body:
		default:
			// A client that cannot keep up is disconnected; the page reloads
			// its state over REST, so dropping is safer than unbounded memory.
			delete(h.clients, client)
			close(client)
		}
	}
}

func (h *Hub) subscribe() (<-chan []byte, func()) {
	client := make(chan []byte, clientBuffer)
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	unsubscribe := func() {
		h.mu.Lock()
		if _, ok := h.clients[client]; ok {
			delete(h.clients, client)
			close(client)
		}
		h.mu.Unlock()
	}
	return client, unsubscribe
}

// handleEvents upgrades to a websocket and streams batches until the client
// goes away.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The service is fronted by an authenticating proxy and is not a
		// cross-site API; accept the origin the proxy forwards.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := conn.CloseRead(r.Context())
	events, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case body, ok := <-events:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "resync")
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, body)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
