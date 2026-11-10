package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/dsiebel/mqttree/internal/broker"
	"github.com/dsiebel/mqttree/internal/store"
)

type publishCall struct {
	connectionID int64
	topic        string
	payload      string
	qos          int
	retain       bool
}

type fakeBroker struct {
	published []publishCall
	started   []int64
	stopped   []int64
}

func (f *fakeBroker) Start(_ context.Context, id int64) error {
	f.started = append(f.started, id)
	return nil
}
func (f *fakeBroker) Stop(id int64)                                    { f.stopped = append(f.stopped, id) }
func (f *fakeBroker) Restart(_ context.Context, _ int64, _ bool) error { return nil }
func (f *fakeBroker) Status(int64) broker.Status                       { return broker.Status{} }
func (f *fakeBroker) Publish(_ context.Context, id int64, topic string, body []byte, qos int, retain bool) error {
	f.published = append(f.published, publishCall{id, topic, string(body), qos, retain})
	return nil
}

func newTestServer(t *testing.T) (*httptest.Server, *store.Store, *fakeBroker, *Hub) {
	t.Helper()
	dir := t.TempDir()
	key, err := store.LoadKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "db.sqlite"), key)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)

	fake := &fakeBroker{}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>ui")}}
	server := httptest.NewServer(New(st, fake, hub, assets).Handler())
	t.Cleanup(server.Close)
	return server, st, fake, hub
}

func doJSON(t *testing.T, method, url string, body string) (*http.Response, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return response, out
}

func TestCreateConnectionPersistsAndStarts(t *testing.T) {
	server, _, fake, _ := newTestServer(t)
	response, body := doJSON(t, http.MethodPost, server.URL+"/api/connections",
		`{"name":"home","host":"192.168.154.50","port":1883,"username":"u","password":"p","topic_filter":"#","qos":1,"enabled":true}`)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %v", response.StatusCode, body)
	}
	if body["has_password"] != true {
		t.Errorf("has_password = %v", body["has_password"])
	}
	if _, leaked := body["password"]; leaked {
		t.Error("response leaked the password")
	}
	if len(fake.started) != 1 {
		t.Errorf("started = %v", fake.started)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/bootstrap", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d", response.StatusCode)
	}
	connections, _ := body["connections"].([]any)
	if len(connections) != 1 {
		t.Fatalf("connections = %v", body["connections"])
	}
	settings, _ := body["settings"].(map[string]any)
	if settings["retention_hours"] != float64(24) {
		t.Errorf("settings = %v", settings)
	}
}

func TestCreateConnectionRejectsInvalidInput(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	for name, payload := range map[string]string{
		"no host":       `{"name":"x","topic_filter":"#"}`,
		"bad port":      `{"name":"x","host":"h","port":70000}`,
		"unknown field": `{"name":"x","host":"h","nope":1}`,
	} {
		response, body := doJSON(t, http.MethodPost, server.URL+"/api/connections", payload)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d body = %v", name, response.StatusCode, body)
		}
	}
}

func TestPublishForwardsToBroker(t *testing.T) {
	server, _, fake, _ := newTestServer(t)
	response, body := doJSON(t, http.MethodPost, server.URL+"/api/publish",
		`{"connection_id":7,"topic":"a/b","payload":"hello","qos":1,"retain":true}`)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d body = %v", response.StatusCode, body)
	}
	if len(fake.published) != 1 {
		t.Fatalf("published = %+v", fake.published)
	}
	call := fake.published[0]
	if call.connectionID != 7 || call.topic != "a/b" || call.payload != "hello" || call.qos != 1 || !call.retain {
		t.Errorf("call = %+v", call)
	}

	response, _ = doJSON(t, http.MethodPost, server.URL+"/api/publish", `{"connection_id":7,"topic":""}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("empty topic status = %d", response.StatusCode)
	}
}

func TestTreeAndHistoryEndpoints(t *testing.T) {
	server, st, _, _ := newTestServer(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, store.Connection{Name: "c", Host: "h", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := st.Record(ctx, connection.ID, []store.Inbound{
		{Topic: "a/b", Payload: []byte(`{"x":1}`), TS: now - 10},
		{Topic: "a/b", Payload: []byte(`{"x":2}`), TS: now},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	response, body := doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/api/tree?connection=%d", server.URL, connection.ID), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tree status = %d", response.StatusCode)
	}
	topics, _ := body["topics"].([]any)
	if len(topics) != 1 {
		t.Fatalf("topics = %v", body["topics"])
	}
	first, _ := topics[0].(map[string]any)
	if first["topic"] != "a/b" || first["text"] != `{"x":2}` {
		t.Errorf("topic = %v", first)
	}

	response, body = doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/api/messages?connection=%d&topic=a%%2Fb&limit=10", server.URL, connection.ID), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d", response.StatusCode)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v", body["messages"])
	}

	response, _ = doJSON(t, http.MethodGet, server.URL+"/api/messages?connection=1", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("missing topic status = %d", response.StatusCode)
	}
}

func TestSettingsEndpointValidates(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	response, body := doJSON(t, http.MethodPut, server.URL+"/api/settings",
		`{"retention_hours":48,"max_db_bytes":1073741824,"max_payload_bytes":1048576}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %v", response.StatusCode, body)
	}
	if body["retention_hours"] != float64(48) {
		t.Errorf("settings = %v", body)
	}

	response, _ = doJSON(t, http.MethodPut, server.URL+"/api/settings",
		`{"retention_hours":0,"max_db_bytes":1,"max_payload_bytes":1}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid settings status = %d", response.StatusCode)
	}
}

func TestEventsStreamBatchesMessages(t *testing.T) {
	server, _, _, hub := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/events"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	hub.Publish(broker.Event{Type: "message", ConnectionID: 1, Message: &broker.Message{Topic: "a/b", PayloadB64: "aGk=", Text: "hi"}})

	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var frame struct {
		Events []broker.Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if len(frame.Events) != 1 || frame.Events[0].Message == nil || frame.Events[0].Message.Topic != "a/b" {
		t.Fatalf("events = %+v", frame.Events)
	}
}

func TestStaticAssetServed(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "ui") {
		t.Errorf("status = %d body = %q", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("missing content security policy")
	}
}

func TestClearConnectionEndpoint(t *testing.T) {
	server, st, _, _ := newTestServer(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, store.Connection{Name: "c", Host: "h", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.Record(ctx, connection.ID, []store.Inbound{
		{Topic: "a/b", Payload: []byte("x"), TS: time.Now().UnixMilli()},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	response, body := doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/connections/%d/clear", server.URL, connection.ID), "")
	if response.StatusCode != http.StatusOK || body["cleared"] != true {
		t.Fatalf("clear status = %d body = %v", response.StatusCode, body)
	}

	response, body = doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/api/tree?connection=%d", server.URL, connection.ID), "")
	topics, _ := body["topics"].([]any)
	if response.StatusCode != http.StatusOK || len(topics) != 0 {
		t.Fatalf("tree after clear = %v (%d)", body["topics"], response.StatusCode)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/bootstrap", "")
	connections, _ := body["connections"].([]any)
	if len(connections) != 1 {
		t.Fatalf("connection removed by clear: %v", body["connections"])
	}

	response, _ = doJSON(t, http.MethodPost, server.URL+"/api/connections/999/clear", "")
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("unknown connection status = %d", response.StatusCode)
	}
}
