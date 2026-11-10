package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	key, err := LoadKey(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	st, err := Open(filepath.Join(dir, "mqttree.db"), key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	key, err := LoadKey(path)
	if err != nil {
		t.Fatalf("load generated key: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("key mode = %o, want 600", mode)
	}

	sealed, err := key.Seal([]byte("broker-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, []byte("broker-password")) {
		t.Fatal("sealed value contains plaintext")
	}
	plaintext, err := key.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plaintext) != "broker-password" {
		t.Errorf("round trip = %q", plaintext)
	}

	other, err := LoadKey(filepath.Join(dir, "other.key"))
	if err != nil {
		t.Fatalf("load other key: %v", err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("decrypting with a different key succeeded")
	}
}

func TestConnectionSecretsAreEncryptedAtRest(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	created, err := st.CreateConnection(ctx, Connection{
		Name: "home", Host: "192.168.154.50", Port: 1883,
		Username: "mqttree", Password: "s3cret-pass", TopicFilter: "#", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.HasPassword {
		t.Error("HasPassword = false after storing a password")
	}
	if created.Password != "" {
		t.Error("password was returned on read")
	}

	var sealed []byte
	if err := st.db.QueryRowContext(ctx,
		`SELECT password_enc FROM connections WHERE id = ?`, created.ID).Scan(&sealed); err != nil {
		t.Fatalf("read sealed password: %v", err)
	}
	if bytes.Contains(sealed, []byte("s3cret-pass")) {
		t.Fatal("database column contains the plaintext password")
	}
	username, password, err := st.Credentials(ctx, created.ID)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if username != "mqttree" || password != "s3cret-pass" {
		t.Errorf("credentials = %q/%q", username, password)
	}

	// An update without a password keeps the stored secret.
	created.Name = "home-renamed"
	created.Password = ""
	updated, err := st.UpdateConnection(ctx, created)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, password, err = st.Credentials(ctx, updated.ID); err != nil || password != "s3cret-pass" {
		t.Fatalf("password lost on update: %q, %v", password, err)
	}

	// An explicit clear removes it.
	updated.ClearPassword = true
	if _, err := st.UpdateConnection(ctx, updated); err != nil {
		t.Fatalf("clear password: %v", err)
	}
	if _, password, err = st.Credentials(ctx, updated.ID); err != nil || password != "" {
		t.Fatalf("password not cleared: %q, %v", password, err)
	}
}

func TestConnectionValidation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	cases := map[string]Connection{
		"missing name":  {Host: "broker"},
		"missing host":  {Name: "x"},
		"bad port":      {Name: "x", Host: "broker", Port: 70000},
		"bad qos":       {Name: "x", Host: "broker", QoS: 3},
		"filter spaces": {Name: "x", Host: "broker", TopicFilter: "a b"},
	}
	for name, connection := range cases {
		if _, err := st.CreateConnection(ctx, connection); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDeleteConnectionCascades(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, Connection{Name: "c", Host: "broker", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.Record(ctx, connection.ID, []Inbound{{Topic: "a/b", Payload: []byte("x"), TS: time.Now().UnixMilli()}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := st.DeleteConnection(ctx, connection.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.ConnectionByID(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ConnectionByID after delete = %v", err)
	}
	topics, err := st.Topics(ctx, connection.ID, "", 10)
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	if len(topics) != 0 {
		t.Errorf("topics after delete = %d", len(topics))
	}
}

func TestRecordTopicsAndHistory(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, Connection{Name: "c", Host: "broker", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := st.Record(ctx, connection.ID, []Inbound{
		{Topic: "sensor/temp", Payload: []byte(`{"v":1}`), QoS: 1, TS: now - 2000},
		{Topic: "sensor/temp", Payload: []byte(`{"v":2}`), QoS: 1, TS: now - 1000},
		{Topic: "sensor/humidity", Payload: []byte(`{"v":3}`), Retained: true, TS: now},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	topics, err := st.Topics(ctx, connection.ID, "", 10)
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("topics = %d, want 2", len(topics))
	}
	if topics[0].Topic != "sensor/humidity" || !topics[0].Retained {
		t.Errorf("first topic = %+v", topics[0])
	}
	if string(topics[1].Payload) != `{"v":2}` || topics[1].ReceivedAt != now-1000 {
		t.Errorf("last state = %+v", topics[1])
	}

	history, err := st.History(ctx, connection.ID, "sensor/temp", 0, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 || string(history[0].Payload) != `{"v":2}` {
		t.Fatalf("history = %+v", history)
	}
	older, err := st.History(ctx, connection.ID, "sensor/temp", history[0].ID, 10)
	if err != nil {
		t.Fatalf("paged history: %v", err)
	}
	if len(older) != 1 || string(older[0].Payload) != `{"v":1}` {
		t.Fatalf("older page = %+v", older)
	}

	filtered, err := st.Topics(ctx, connection.ID, "sensor/h", 10)
	if err != nil {
		t.Fatalf("prefixed topics: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Topic != "sensor/humidity" {
		t.Errorf("prefix match = %+v", filtered)
	}
}

func TestPruneHonoursMessageExpiryAndRetention(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, Connection{Name: "c", Host: "broker", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UnixMilli()

	// MQTT v5 expiry in the past: pruned even though it is recent.
	if err := st.Record(ctx, connection.ID, []Inbound{
		{Topic: "expired", Payload: []byte("x"), TS: now, ExpiresAt: now - 1},
	}); err != nil {
		t.Fatalf("record expired: %v", err)
	}
	// Older than the retention window: pruned by timestamp.
	if _, err := st.SaveSettings(ctx, Settings{RetentionHours: 1, MaxDBBytes: 1 << 30, MaxPayloadBytes: 1 << 20}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if err := st.Record(ctx, connection.ID, []Inbound{
		{Topic: "old", Payload: []byte("x"), TS: now - 2*60*60*1000, ExpiresAt: now + 60*60*1000},
		{Topic: "fresh", Payload: []byte("x"), TS: now},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	if _, err := st.Prune(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	topics, err := st.Topics(ctx, connection.ID, "", 10)
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	if len(topics) != 1 || topics[0].Topic != "fresh" {
		t.Fatalf("topics after prune = %+v", topics)
	}
}

func TestPruneEnforcesDatabaseCap(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, Connection{Name: "c", Host: "broker", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.SaveSettings(ctx, Settings{RetentionHours: 24, MaxDBBytes: 16 << 20, MaxPayloadBytes: 1 << 20}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	now := time.Now().UnixMilli()
	payload := bytes.Repeat([]byte("x"), 64<<10)
	for range 400 {
		if err := st.Record(ctx, connection.ID, []Inbound{{Topic: "bulk", Payload: payload, TS: now}}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if _, err := st.Prune(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.DBBytes > 16<<20 {
		t.Errorf("db size = %d, want <= 16 MiB", stats.DBBytes)
	}
	if stats.Messages == 0 {
		t.Error("prune deleted every message")
	}
}

func TestPayloadTruncation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, Connection{Name: "c", Host: "broker", TopicFilter: "#", Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.SaveSettings(ctx, Settings{RetentionHours: 24, MaxDBBytes: 1 << 30, MaxPayloadBytes: 1024}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if err := st.Record(ctx, connection.ID, []Inbound{{Topic: "big", Payload: bytes.Repeat([]byte("a"), 4096), TS: time.Now().UnixMilli()}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	topics, err := st.Topics(ctx, connection.ID, "", 10)
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	if len(topics) != 1 || !topics[0].Truncated || len(topics[0].Payload) != 1024 {
		t.Fatalf("topic = %+v (len %d)", topics[0], len(topics[0].Payload))
	}
}

func TestSettingsValidation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	settings, err := st.Settings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.RetentionHours != 24 || settings.MaxDBBytes != 1<<30 {
		t.Errorf("defaults = %+v", settings)
	}
	if _, err := st.SaveSettings(ctx, Settings{RetentionHours: 0, MaxDBBytes: 1 << 20, MaxPayloadBytes: 1 << 10}); err == nil {
		t.Error("expected an error for a too small database cap")
	}
	if _, err := st.SaveSettings(ctx, Settings{RetentionHours: 10_000, MaxDBBytes: 1 << 30, MaxPayloadBytes: 1 << 20}); err == nil {
		t.Error("expected an error for an out of range retention")
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")
	key, err := LoadKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	st, err := Open(path, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE schema_version SET version = 99`); err != nil {
		t.Fatalf("bump schema: %v", err)
	}
	st.Close()
	if _, err := Open(path, key); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("open with newer schema = %v", err)
	}
}

func TestClearConnectionKeepsDefinitionAndCredentials(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	connection, err := st.CreateConnection(ctx, Connection{
		Name: "c", Host: "broker", Username: "u", Password: "p", TopicFilter: "#", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.Record(ctx, connection.ID, []Inbound{
		{Topic: "a/b", Payload: []byte("x"), TS: time.Now().UnixMilli()},
		{Topic: "a/c", Payload: []byte("y"), TS: time.Now().UnixMilli()},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := st.ClearConnection(ctx, connection.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	topics, err := st.Topics(ctx, connection.ID, "", 10)
	if err != nil || len(topics) != 0 {
		t.Fatalf("topics after clear = %v, %v", topics, err)
	}
	history, err := st.History(ctx, connection.ID, "a/b", 0, 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("history after clear = %v, %v", history, err)
	}
	if _, err := st.ConnectionByID(ctx, connection.ID); err != nil {
		t.Fatalf("connection lost on clear: %v", err)
	}
	if _, password, err := st.Credentials(ctx, connection.ID); err != nil || password != "p" {
		t.Fatalf("credentials lost on clear: %q, %v", password, err)
	}
}
