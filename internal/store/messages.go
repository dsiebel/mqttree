package store

import (
	"context"
	"fmt"
	"time"
)

// Inbound is a message as received from a broker.
type Inbound struct {
	Topic     string
	Payload   []byte
	QoS       int
	Retained  bool
	TS        int64
	ExpiresAt int64 // absolute epoch milliseconds; 0 means "use retention"
}

// Message is a stored message. The payload is handed to the API layer, which
// decides how to render it.
type Message struct {
	ID        int64  `json:"id"`
	Topic     string `json:"topic"`
	TS        int64  `json:"ts"`
	QoS       int    `json:"qos"`
	Retained  bool   `json:"retained"`
	Truncated bool   `json:"truncated"`
	Payload   []byte `json:"-"`
}

// Topic is the last known state of one topic.
type Topic struct {
	Topic      string `json:"topic"`
	QoS        int    `json:"qos"`
	Retained   bool   `json:"retained"`
	Truncated  bool   `json:"truncated"`
	ReceivedAt int64  `json:"received_at"`
	Payload    []byte `json:"-"`
}

// Record persists a batch of inbound messages and updates the topic state in
// the same transaction. Payloads beyond the configured cap are truncated and
// flagged rather than dropped.
func (s *Store) Record(ctx context.Context, connectionID int64, messages []Inbound) error {
	if len(messages) == 0 {
		return nil
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ingest transaction: %w", err)
	}
	defer tx.Rollback()

	insert, err := tx.PrepareContext(ctx, `
INSERT INTO messages(connection_id, topic, ts, expires_at, qos, retained, truncated, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare message insert: %w", err)
	}
	defer insert.Close()
	upsert, err := tx.PrepareContext(ctx, `
INSERT INTO topics(connection_id, topic, payload, qos, retained, truncated, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(connection_id, topic) DO UPDATE SET
    payload = excluded.payload,
    qos = excluded.qos,
    retained = excluded.retained,
    truncated = excluded.truncated,
    received_at = excluded.received_at
WHERE excluded.received_at >= topics.received_at`)
	if err != nil {
		return fmt.Errorf("prepare topic upsert: %w", err)
	}
	defer upsert.Close()

	for _, message := range messages {
		payload := message.Payload
		if payload == nil {
			payload = []byte{}
		}
		truncated := len(payload) > settings.MaxPayloadBytes
		if truncated {
			payload = payload[:settings.MaxPayloadBytes]
		}
		ts := message.TS
		if ts == 0 {
			ts = now
		}
		expires := message.ExpiresAt
		if expires == 0 {
			expires = ts + settings.Retained()
		}
		if _, err := insert.ExecContext(ctx, connectionID, message.Topic, ts, expires,
			message.QoS, boolInt(message.Retained), boolInt(truncated), payload); err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		if _, err := upsert.ExecContext(ctx, connectionID, message.Topic, payload,
			message.QoS, boolInt(message.Retained), boolInt(truncated), ts); err != nil {
			return fmt.Errorf("upsert topic: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ingest batch: %w", err)
	}
	return nil
}

// Topics returns the topic tree rows for one connection. prefix is a literal
// prefix match, not a topic wildcard, so search stays predictable.
func (s *Store) Topics(ctx context.Context, connectionID int64, prefix string, limit int) ([]Topic, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	query := `SELECT topic, qos, retained, truncated, received_at, payload
	          FROM topics WHERE connection_id = ?`
	args := []any{connectionID}
	if prefix != "" {
		query += ` AND substr(topic, 1, ?) = ?`
		args = append(args, len(prefix), prefix)
	}
	query += ` ORDER BY topic LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	defer rows.Close()
	out := []Topic{}
	for rows.Next() {
		var (
			t                   Topic
			retained, truncated int
			payload             []byte
		)
		if err := rows.Scan(&t.Topic, &t.QoS, &retained, &truncated, &t.ReceivedAt, &payload); err != nil {
			return nil, fmt.Errorf("scan topic: %w", err)
		}
		t.Retained = retained == 1
		t.Truncated = truncated == 1
		t.Payload = payload
		out = append(out, t)
	}
	return out, rows.Err()
}

// History returns stored messages for one topic, newest first. beforeID pages
// backwards: pass the smallest id already seen, or 0 for the newest page.
func (s *Store) History(ctx context.Context, connectionID int64, topic string, beforeID int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, topic, ts, qos, retained, truncated, payload
	          FROM messages WHERE connection_id = ? AND topic = ?`
	args := []any{connectionID, topic}
	if beforeID > 0 {
		query += ` AND id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var (
			m                   Message
			retained, truncated int
			payload             []byte
		)
		if err := rows.Scan(&m.ID, &m.Topic, &m.TS, &m.QoS, &retained, &truncated, &payload); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.Retained = retained == 1
		m.Truncated = truncated == 1
		m.Payload = payload
		out = append(out, m)
	}
	return out, rows.Err()
}

// ClearConnection deletes every stored topic and message for one connection,
// leaving the connection definition (and its credentials) untouched.
func (s *Store) ClearConnection(ctx context.Context, connectionID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE connection_id = ?`, connectionID); err != nil {
		return fmt.Errorf("clear messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM topics WHERE connection_id = ?`, connectionID); err != nil {
		return fmt.Errorf("clear topics: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clear: %w", err)
	}
	return nil
}
