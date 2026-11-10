// Package api exposes the REST and websocket surface plus the embedded UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dsiebel/mqttree/internal/broker"
	"github.com/dsiebel/mqttree/internal/payload"
	"github.com/dsiebel/mqttree/internal/store"
)

const maxPublishPayload = 4 << 20

// Broker is the subset of the session manager the API needs.
type Broker interface {
	Start(ctx context.Context, id int64) error
	Stop(id int64)
	Restart(ctx context.Context, id int64, wasRunning bool) error
	Status(id int64) broker.Status
	Publish(ctx context.Context, id int64, topic string, body []byte, qos int, retain bool) error
}

// Server wires the store, the broker sessions and the hub into an http.Handler.
type Server struct {
	store  *store.Store
	broker Broker
	hub    *Hub
	assets fs.FS
}

// New builds the server.
func New(st *store.Store, br Broker, hub *Hub, assets fs.FS) *Server {
	return &Server{store: st, broker: br, hub: hub, assets: assets}
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	mux.HandleFunc("GET /api/connections", s.handleListConnections)
	mux.HandleFunc("POST /api/connections", s.handleCreateConnection)
	mux.HandleFunc("PUT /api/connections/{id}", s.handleUpdateConnection)
	mux.HandleFunc("DELETE /api/connections/{id}", s.handleDeleteConnection)
	mux.HandleFunc("POST /api/connections/{id}/start", s.handleStartConnection)
	mux.HandleFunc("POST /api/connections/{id}/stop", s.handleStopConnection)
	mux.HandleFunc("POST /api/connections/{id}/clear", s.handleClearConnection)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/messages", s.handleMessages)
	mux.HandleFunc("POST /api/publish", s.handlePublish)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.Handle("GET /", http.FileServerFS(s.assets))
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic in handler", "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			slog.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).Round(time.Millisecond))
		}
	})
}

type bootstrapResponse struct {
	Connections []store.Connection       `json:"connections"`
	Statuses    map[string]broker.Status `json:"statuses"`
	Settings    store.Settings           `json:"settings"`
	Stats       store.Stats              `json:"stats"`
	ServerTime  int64                    `json:"server_time"`
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	connections, err := s.store.ListConnections(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	statuses := make(map[string]broker.Status, len(connections))
	for _, connection := range connections {
		statuses[strconv.FormatInt(connection.ID, 10)] = s.broker.Status(connection.ID)
	}
	writeJSON(w, http.StatusOK, bootstrapResponse{
		Connections: connections,
		Statuses:    statuses,
		Settings:    settings,
		Stats:       stats,
		ServerTime:  time.Now().UnixMilli(),
	})
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	connections, err := s.store.ListConnections(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	statuses := make(map[string]broker.Status, len(connections))
	for _, connection := range connections {
		statuses[strconv.FormatInt(connection.ID, 10)] = s.broker.Status(connection.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": connections, "statuses": statuses})
}

func (s *Server) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	var connection store.Connection
	if err := decodeJSON(w, r, &connection); err != nil {
		return
	}
	created, err := s.store.CreateConnection(r.Context(), connection)
	if err != nil {
		if isValidation(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, err)
		return
	}
	if created.Enabled {
		if err := s.broker.Start(r.Context(), created.ID); err != nil {
			slog.Error("start new connection", "connection", created.Name, "error", err)
		}
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var connection store.Connection
	if err := decodeJSON(w, r, &connection); err != nil {
		return
	}
	connection.ID = id
	wasRunning := s.broker.Status(id).Running
	updated, err := s.store.UpdateConnection(r.Context(), connection)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "connection not found")
		case isValidation(err):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			fail(w, err)
		}
		return
	}
	if err := s.broker.Restart(r.Context(), id, wasRunning && updated.Enabled); err != nil {
		slog.Error("restart connection", "connection", updated.Name, "error", err)
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broker.Stop(id)
	if err := s.store.DeleteConnection(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartConnection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.broker.Start(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.broker.Status(id))
}

func (s *Server) handleStopConnection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broker.Stop(id)
	writeJSON(w, http.StatusOK, s.broker.Status(id))
}

func (s *Server) handleClearConnection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.ConnectionByID(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		fail(w, err)
		return
	}
	if err := s.store.ClearConnection(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

type topicDTO struct {
	Topic      string `json:"topic"`
	QoS        int    `json:"qos"`
	Retained   bool   `json:"retained"`
	Truncated  bool   `json:"truncated"`
	ReceivedAt int64  `json:"received_at"`
	PayloadB64 string `json:"payload_b64"`
	Text       string `json:"text"`
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	connectionID, err := queryInt(r, "connection")
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection query parameter is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	topics, err := s.store.Topics(r.Context(), connectionID, r.URL.Query().Get("prefix"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]topicDTO, 0, len(topics))
	for _, topic := range topics {
		encoded, text := payload.Render(topic.Payload)
		out = append(out, topicDTO{
			Topic:      topic.Topic,
			QoS:        topic.QoS,
			Retained:   topic.Retained,
			Truncated:  topic.Truncated,
			ReceivedAt: topic.ReceivedAt,
			PayloadB64: encoded,
			Text:       text,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": out})
}

type messageDTO struct {
	ID         int64  `json:"id"`
	Topic      string `json:"topic"`
	TS         int64  `json:"ts"`
	QoS        int    `json:"qos"`
	Retained   bool   `json:"retained"`
	Truncated  bool   `json:"truncated"`
	PayloadB64 string `json:"payload_b64"`
	Text       string `json:"text"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	connectionID, err := queryInt(r, "connection")
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection query parameter is required")
		return
	}
	topic := r.URL.Query().Get("topic")
	if topic == "" {
		writeError(w, http.StatusBadRequest, "topic query parameter is required")
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	messages, err := s.store.History(r.Context(), connectionID, topic, before, limit)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]messageDTO, 0, len(messages))
	for _, message := range messages {
		encoded, text := payload.Render(message.Payload)
		out = append(out, messageDTO{
			ID:         message.ID,
			Topic:      message.Topic,
			TS:         message.TS,
			QoS:        message.QoS,
			Retained:   message.Retained,
			Truncated:  message.Truncated,
			PayloadB64: encoded,
			Text:       text,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

type publishRequest struct {
	ConnectionID int64  `json:"connection_id"`
	Topic        string `json:"topic"`
	Payload      string `json:"payload"`
	QoS          int    `json:"qos"`
	Retain       bool   `json:"retain"`
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var request publishRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if strings.TrimSpace(request.Topic) == "" {
		writeError(w, http.StatusBadRequest, "topic is required")
		return
	}
	if len(request.Payload) > maxPublishPayload {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	if err := s.broker.Publish(r.Context(), request.ConnectionID, request.Topic,
		[]byte(request.Payload), request.QoS, request.Retain); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"published": true})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var settings store.Settings
	if err := decodeJSON(w, r, &settings); err != nil {
		return
	}
	saved, err := s.store.SaveSettings(r.Context(), settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid connection id")
	}
	return id, nil
}

func queryInt(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublishPayload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return err
	}
	return nil
}

func isValidation(err error) bool {
	message := err.Error()
	for _, marker := range []string{
		"is required", "must be between", "must be 0, 1 or 2", "must not contain", "requires tls",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func fail(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
