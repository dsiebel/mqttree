// Package store owns the SQLite database: saved connections, the last known
// state per topic and the retained message history.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS connections (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL UNIQUE,
    host         TEXT    NOT NULL,
    port         INTEGER NOT NULL,
    client_id    TEXT    NOT NULL DEFAULT '',
    username     TEXT    NOT NULL DEFAULT '',
    password_enc BLOB,
    tls          INTEGER NOT NULL DEFAULT 0,
    tls_insecure INTEGER NOT NULL DEFAULT 0,
    ca_pem       TEXT    NOT NULL DEFAULT '',
    topic_filter TEXT    NOT NULL DEFAULT '#',
    qos          INTEGER NOT NULL DEFAULT 0,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS topics (
    connection_id INTEGER NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    topic         TEXT    NOT NULL,
    payload       BLOB,
    qos           INTEGER NOT NULL DEFAULT 0,
    retained      INTEGER NOT NULL DEFAULT 0,
    truncated     INTEGER NOT NULL DEFAULT 0,
    received_at   INTEGER NOT NULL,
    PRIMARY KEY (connection_id, topic)
);

CREATE TABLE IF NOT EXISTS messages (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    connection_id INTEGER NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    topic         TEXT    NOT NULL,
    ts            INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    qos           INTEGER NOT NULL DEFAULT 0,
    retained      INTEGER NOT NULL DEFAULT 0,
    truncated     INTEGER NOT NULL DEFAULT 0,
    payload       BLOB
);

CREATE INDEX IF NOT EXISTS messages_by_topic ON messages(connection_id, topic, id DESC);
CREATE INDEX IF NOT EXISTS messages_by_expiry ON messages(expires_at);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// Store is a handle on the SQLite database and the credential key.
type Store struct {
	db   *sql.DB
	key  *Key
	path string
}

// Open creates the data directory, opens the database and applies the schema.
func Open(path string, key *Key) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection keeps write batches, prune work and API reads strictly
	// ordered, which removes the SQLITE_BUSY paths a concurrent writer set
	// would introduce. Throughput is bounded by the 250ms ingest batch, so a
	// single writer is not the bottleneck at homelab scale.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	s := &Store{db: db, key: key, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (1)`); err != nil {
			return fmt.Errorf("record schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	case version != 1:
		return fmt.Errorf("database schema version %d is newer than this build", version)
	}
	defaults := DefaultSettings()
	for key, value := range map[string]string{
		"retention_hours":   fmt.Sprint(defaults.RetentionHours),
		"max_db_bytes":      fmt.Sprint(defaults.MaxDBBytes),
		"max_payload_bytes": fmt.Sprint(defaults.MaxPayloadBytes),
	} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, value); err != nil {
			return fmt.Errorf("seed setting %s: %w", key, err)
		}
	}
	return nil
}

// Settings returns the operator-tunable runtime configuration.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()
	out := DefaultSettings()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Settings{}, fmt.Errorf("scan setting: %w", err)
		}
		switch key {
		case "retention_hours":
			fmt.Sscan(value, &out.RetentionHours)
		case "max_db_bytes":
			fmt.Sscan(value, &out.MaxDBBytes)
		case "max_payload_bytes":
			fmt.Sscan(value, &out.MaxPayloadBytes)
		}
	}
	return out.Normalize()
}

// SaveSettings validates and persists the runtime configuration.
func (s *Store) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	normalized, err := settings.Normalize()
	if err != nil {
		return Settings{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("begin settings transaction: %w", err)
	}
	defer tx.Rollback()
	for key, value := range map[string]string{
		"retention_hours":   fmt.Sprint(normalized.RetentionHours),
		"max_db_bytes":      fmt.Sprint(normalized.MaxDBBytes),
		"max_payload_bytes": fmt.Sprint(normalized.MaxPayloadBytes),
	} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			key, value); err != nil {
			return Settings{}, fmt.Errorf("save setting %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, fmt.Errorf("commit settings: %w", err)
	}
	return normalized, nil
}

// Stats summarises what the store currently holds.
type Stats struct {
	Connections int   `json:"connections"`
	Topics      int   `json:"topics"`
	Messages    int   `json:"messages"`
	DBBytes     int64 `json:"db_bytes"`
}

// Stats returns row counts and the live database size.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	for query, dest := range map[string]*int{
		`SELECT COUNT(*) FROM connections`: &out.Connections,
		`SELECT COUNT(*) FROM topics`:      &out.Topics,
		`SELECT COUNT(*) FROM messages`:    &out.Messages,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(dest); err != nil {
			return Stats{}, fmt.Errorf("read stats: %w", err)
		}
	}
	size, err := s.liveBytes(ctx)
	if err != nil {
		return Stats{}, err
	}
	out.DBBytes = size
	return out, nil
}

// liveBytes is (page_count - freelist_count) * page_size, so a file that has
// not been vacuumed does not look full when its pages are free.
func (s *Store) liveBytes(ctx context.Context) (int64, error) {
	var pageCount, pageSize, freelist int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("read page count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("read page size: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freelist); err != nil {
		return 0, fmt.Errorf("read freelist count: %w", err)
	}
	live := (pageCount - freelist) * pageSize
	if live < 0 {
		return 0, nil
	}
	return live, nil
}

// Prune deletes expired messages and topics that no longer have a message and
// are past the retention window, then enforces the database size cap. It is
// chunked so each DELETE is a short transaction.
func (s *Store) Prune(ctx context.Context, now int64) (int64, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := now - settings.Retained()
	var deleted int64
	for range 8 {
		res, err := s.db.ExecContext(ctx, `
DELETE FROM messages WHERE id IN (
    SELECT id FROM messages WHERE expires_at < ? OR ts < ? ORDER BY id LIMIT 5000
)`, now, cutoff)
		if err != nil {
			return deleted, fmt.Errorf("prune messages: %w", err)
		}
		affected, _ := res.RowsAffected()
		deleted += affected
		if affected < 5000 {
			break
		}
	}
	// A topic row only survives while it has history behind it. Retained
	// topics keep their last state until the retention window closes, since
	// the retained payload itself is broker state rather than traffic.
	if _, err := s.db.ExecContext(ctx, `
DELETE FROM topics
WHERE NOT EXISTS (
      SELECT 1 FROM messages m WHERE m.connection_id = topics.connection_id AND m.topic = topics.topic
  )
  AND (retained = 0 OR received_at < ?)`, cutoff); err != nil {
		return deleted, fmt.Errorf("prune topics: %w", err)
	}
	for range 64 {
		size, err := s.liveBytes(ctx)
		if err != nil {
			return deleted, err
		}
		if size <= settings.MaxDBBytes {
			break
		}
		// Small chunks: the cap is a byte budget, not a row budget, and one
		// large DELETE can overshoot and empty the history.
		res, err := s.db.ExecContext(ctx, `
DELETE FROM messages WHERE id IN (SELECT id FROM messages ORDER BY id LIMIT 100)`)
		if err != nil {
			return deleted, fmt.Errorf("prune for size: %w", err)
		}
		affected, _ := res.RowsAffected()
		deleted += affected
		if affected == 0 {
			break
		}
	}
	return deleted, nil
}
