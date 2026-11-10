// Package broker owns the live MQTT sessions. One autopaho connection runs per
// enabled saved connection; inbound messages are batched into the store and
// forwarded to the websocket hub.
package broker

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/dsiebel/mqttree/internal/payload"
	"github.com/dsiebel/mqttree/internal/store"
)

const (
	flushInterval = 250 * time.Millisecond
	queueSize     = 8192
)

// Status is the live state of one broker session.
type Status struct {
	Running   bool   `json:"running"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
	Dropped   uint64 `json:"dropped"`
}

// Message is an inbound message forwarded to the UI.
type Message struct {
	Topic      string `json:"topic"`
	TS         int64  `json:"ts"`
	QoS        int    `json:"qos"`
	Retained   bool   `json:"retained"`
	Truncated  bool   `json:"truncated"`
	PayloadB64 string `json:"payload_b64"`
	Text       string `json:"text,omitempty"`
}

// Event is what the hub broadcasts.
type Event struct {
	Type         string   `json:"type"`
	ConnectionID int64    `json:"connection_id"`
	Message      *Message `json:"message,omitempty"`
	Status       *Status  `json:"status,omitempty"`
}

// Manager runs the sessions.
type Manager struct {
	store *store.Store
	emit  func(Event)

	mu       sync.Mutex
	sessions map[int64]*session
}

type session struct {
	id       int64
	conn     store.Connection
	cm       *autopaho.ConnectionManager
	cancel   context.CancelFunc
	inbound  chan inbound
	done     chan struct{}
	emitFn   func(Event)
	statusMu sync.Mutex
	status   Status
}

type inbound struct {
	message store.Inbound
	text    string
}

// New returns a manager that reports events through emit.
func New(st *store.Store, emit func(Event)) *Manager {
	return &Manager{
		store:    st,
		emit:     emit,
		sessions: map[int64]*session{},
	}
}

// StartAll brings up every enabled connection, which is what makes the topic
// tree survive a restart without operator interaction.
func (m *Manager) StartAll(ctx context.Context) {
	connections, err := m.store.ListConnections(ctx)
	if err != nil {
		slog.Error("list connections for startup", "error", err)
		return
	}
	for _, connection := range connections {
		if !connection.Enabled {
			continue
		}
		if err := m.Start(ctx, connection.ID); err != nil {
			slog.Error("start connection", "connection", connection.Name, "error", err)
		}
	}
}

// Start opens the session for one saved connection. Starting an already
// running session is a no-op.
func (m *Manager) Start(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, running := m.sessions[id]; running {
		return nil
	}
	connection, err := m.store.ConnectionByID(ctx, id)
	if err != nil {
		return err
	}
	username, password, err := m.store.Credentials(ctx, id)
	if err != nil {
		return err
	}
	serverURL, err := brokerURL(connection)
	if err != nil {
		return err
	}
	tlsConfig, err := tlsConfigFor(connection)
	if err != nil {
		return err
	}
	clientID := connection.ClientID
	if clientID == "" {
		clientID = fmt.Sprintf("mqttree-%d-%s", id, randomSuffix())
	}

	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &session{
		id:      id,
		conn:    connection,
		cancel:  cancel,
		emitFn:  m.emit,
		inbound: make(chan inbound, queueSize),
		done:    make(chan struct{}),
	}

	autopahoConfig := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{serverURL},
		KeepAlive:                     30,
		CleanStartOnInitialConnection: false,
		SessionExpiryInterval:         60,
		ConnectUsername:               username,
		ConnectPassword:               []byte(password),
		TlsCfg:                        tlsConfig,
		OnConnectionUp: func(connectionManager *autopaho.ConnectionManager, _ *paho.Connack) {
			s.setConnected(true, "")
			subscribeCtx, subscribeCancel := context.WithTimeout(sessionCtx, 10*time.Second)
			defer subscribeCancel()
			if _, err := connectionManager.Subscribe(subscribeCtx, &paho.Subscribe{
				Subscriptions: []paho.SubscribeOptions{{
					Topic: connection.TopicFilter,
					QoS:   byte(connection.QoS),
				}},
			}); err != nil {
				slog.Error("subscribe failed", "connection", connection.Name, "filter", connection.TopicFilter, "error", err)
				s.setConnected(false, err.Error())
			}
		},
		OnConnectError: func(err error) {
			s.setConnected(false, err.Error())
		},
		ClientConfig: paho.ClientConfig{
			ClientID: clientID,
			OnClientError: func(err error) {
				if !errors.Is(err, context.Canceled) {
					s.setConnected(false, err.Error())
				}
			},
			OnServerDisconnect: func(disconnect *paho.Disconnect) {
				s.setConnected(false, fmt.Sprintf("broker disconnected: 0x%02x", disconnect.ReasonCode))
			},
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(received paho.PublishReceived) (bool, error) {
					s.receive(received.Packet)
					return true, nil
				},
			},
		},
	}

	connectionManager, err := autopaho.NewConnection(sessionCtx, autopahoConfig)
	if err != nil {
		cancel()
		return fmt.Errorf("connect %s: %w", connection.Name, err)
	}
	s.cm = connectionManager
	m.sessions[id] = s
	s.setConnected(false, "")

	go m.flushLoop(sessionCtx, s)
	go func() {
		<-sessionCtx.Done()
		close(s.done)
	}()
	slog.Info("broker session started", "connection", connection.Name, "server", serverURL.String(), "filter", connection.TopicFilter)
	m.emitStatus(s)
	return nil
}

// Stop tears down one session and flushes what is buffered.
func (m *Manager) Stop(id int64) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return
	}
	if s.cm != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.cm.Disconnect(closeCtx)
		cancel()
	}
	s.cancel()
	<-s.done
	s.statusMu.Lock()
	s.status.Running = false
	s.status.Connected = false
	status := s.status
	s.statusMu.Unlock()
	m.emit(Event{Type: "status", ConnectionID: id, Status: &status})
	slog.Info("broker session stopped", "connection", s.conn.Name)
}

// Restart stops and starts a session, used when its definition changes.
func (m *Manager) Restart(ctx context.Context, id int64, wasRunning bool) error {
	m.Stop(id)
	if !wasRunning {
		return nil
	}
	return m.Start(ctx, id)
}

// Status reports the session state for one connection.
func (m *Manager) Status(connectionID int64) Status {
	m.mu.Lock()
	s, ok := m.sessions[connectionID]
	m.mu.Unlock()
	if !ok {
		return Status{}
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

// Publish sends a message through a running session.
func (m *Manager) Publish(ctx context.Context, connectionID int64, topic string, body []byte, qos int, retain bool) error {
	m.mu.Lock()
	s, ok := m.sessions[connectionID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("connection %d is not running", connectionID)
	}
	if qos < 0 || qos > 2 {
		return fmt.Errorf("qos must be 0, 1 or 2")
	}
	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.cm.Publish(publishCtx, &paho.Publish{
		Topic:   topic,
		QoS:     byte(qos),
		Retain:  retain,
		Payload: body,
	})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	return nil
}

// Shutdown stops every session.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

func (s *session) receive(message *paho.Publish) {
	if message == nil || message.Topic == "" {
		return
	}
	copied := make([]byte, len(message.Payload))
	copy(copied, message.Payload)
	ts := time.Now().UnixMilli()
	var expiresAt int64
	if message.Properties != nil && message.Properties.MessageExpiry != nil {
		expiresAt = ts + int64(*message.Properties.MessageExpiry)*1000
	}
	_, text := payload.Render(copied)
	select {
	case s.inbound <- inbound{
		message: store.Inbound{
			Topic:     message.Topic,
			Payload:   copied,
			QoS:       int(message.QoS),
			Retained:  message.Retain,
			TS:        ts,
			ExpiresAt: expiresAt,
		},
		text: text,
	}:
	default:
		s.statusMu.Lock()
		s.status.Dropped++
		s.statusMu.Unlock()
	}
}

func (m *Manager) flushLoop(ctx context.Context, s *session) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	pending := make([]inbound, 0, 256)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := make([]store.Inbound, 0, len(pending))
		for _, item := range pending {
			batch = append(batch, item.message)
		}
		if err := m.store.Record(ctx, s.id, batch); err != nil {
			slog.Error("record messages", "connection", s.conn.Name, "error", err)
		}
		for _, item := range pending {
			encoded, text := payload.Render(item.message.Payload)
			if item.text != "" {
				text = item.text
			}
			m.emit(Event{
				Type:         "message",
				ConnectionID: s.id,
				Message: &Message{
					Topic:      item.message.Topic,
					TS:         item.message.TS,
					QoS:        item.message.QoS,
					Retained:   item.message.Retained,
					PayloadB64: encoded,
					Text:       text,
				},
			})
		}
		pending = pending[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case item := <-s.inbound:
			pending = append(pending, item)
			if len(pending) < 256 {
				continue
			}
			flush()
		case <-ticker.C:
			flush()
		}
	}
}

func (m *Manager) emitStatus(s *session) {
	s.statusMu.Lock()
	status := s.status
	s.statusMu.Unlock()
	m.emit(Event{Type: "status", ConnectionID: s.id, Status: &status})
}

func (s *session) setConnected(connected bool, message string) {
	s.statusMu.Lock()
	changed := s.status.Connected != connected || s.status.Error != message
	s.status.Running = true
	s.status.Connected = connected
	s.status.Error = message
	status := s.status
	s.statusMu.Unlock()
	if !changed {
		return
	}
	slog.Debug("broker status", "connection", s.conn.Name, "connected", connected, "error", message)
	if s.emitFn != nil {
		s.emitFn(Event{Type: "status", ConnectionID: s.id, Status: &status})
	}
}

func brokerURL(c store.Connection) (*url.URL, error) {
	scheme := "mqtt"
	if c.TLS {
		scheme = "tls"
	}
	return url.Parse(fmt.Sprintf("%s://%s:%d", scheme, c.Host, c.Port))
}

func tlsConfigFor(c store.Connection) (*tls.Config, error) {
	if !c.TLS {
		if c.CAPEM == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("a CA certificate requires tls to be enabled")
	}
	config := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: c.TLSInsecure, //nolint:gosec // explicit operator opt-in for self-signed brokers
	}
	if c.CAPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.CAPEM)) {
			return nil, fmt.Errorf("ca_pem does not contain a valid certificate")
		}
		config.RootCAs = pool
	}
	return config, nil
}

func randomSuffix() string {
	buf := make([]byte, 4)
	// crypto/rand.Read fills buf or panics on platforms the standard library
	// supports; the error return is kept for completeness.
	_, _ = cryptorand.Read(buf)
	return hex.EncodeToString(buf)
}
