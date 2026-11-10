package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Connection is a saved broker connection. Password is write-only: reads report
// HasPassword instead of the secret.
type Connection struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	ClientID      string `json:"client_id"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	HasPassword   bool   `json:"has_password"`
	ClearPassword bool   `json:"clear_password,omitempty"`
	TLS           bool   `json:"tls"`
	TLSInsecure   bool   `json:"tls_insecure"`
	CAPEM         string `json:"ca_pem"`
	TopicFilter   string `json:"topic_filter"`
	QoS           int    `json:"qos"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// Validate applies defaults and rejects unusable connection definitions.
func (c *Connection) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Host = strings.TrimSpace(c.Host)
	c.TopicFilter = strings.TrimSpace(c.TopicFilter)
	switch {
	case c.Name == "":
		return fmt.Errorf("name is required")
	case c.Host == "":
		return fmt.Errorf("host is required")
	case strings.ContainsAny(c.TopicFilter, " \t\n"):
		return fmt.Errorf("topic filter must not contain whitespace")
	case c.TopicFilter == "":
		c.TopicFilter = "#"
	}
	if c.Port == 0 {
		if c.TLS {
			c.Port = 8883
		} else {
			c.Port = 1883
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if c.QoS < 0 || c.QoS > 2 {
		return fmt.Errorf("qos must be 0, 1 or 2")
	}
	return nil
}

const connectionColumns = `id, name, host, port, client_id, username,
    (password_enc IS NOT NULL AND length(password_enc) > 0), tls, tls_insecure,
    ca_pem, topic_filter, qos, enabled, created_at, updated_at`

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var (
		c                Connection
		hasPassword      int
		tls, tlsInsecure int
		enabled          int
	)
	err := row.Scan(&c.ID, &c.Name, &c.Host, &c.Port, &c.ClientID, &c.Username, &hasPassword,
		&tls, &tlsInsecure, &c.CAPEM, &c.TopicFilter, &c.QoS, &enabled, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Connection{}, err
	}
	c.HasPassword = hasPassword == 1
	c.TLS = tls == 1
	c.TLSInsecure = tlsInsecure == 1
	c.Enabled = enabled == 1
	return c, nil
}

// ListConnections returns every saved connection ordered by name.
func (s *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM connections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConnectionByID returns one connection or ErrNotFound.
func (s *Store) ConnectionByID(ctx context.Context, id int64) (Connection, error) {
	c, err := scanConnection(s.db.QueryRowContext(ctx,
		`SELECT `+connectionColumns+` FROM connections WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, fmt.Errorf("read connection: %w", err)
	}
	return c, nil
}

// CreateConnection stores a new connection, sealing its password.
func (s *Store) CreateConnection(ctx context.Context, c Connection) (Connection, error) {
	if err := c.Validate(); err != nil {
		return Connection{}, err
	}
	sealed, err := s.sealPassword(c.Password)
	if err != nil {
		return Connection{}, err
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO connections(name, host, port, client_id, username, password_enc, tls, tls_insecure,
                        ca_pem, topic_filter, qos, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Host, c.Port, c.ClientID, c.Username, sealed, boolInt(c.TLS), boolInt(c.TLSInsecure),
		c.CAPEM, c.TopicFilter, c.QoS, boolInt(c.Enabled), now, now)
	if err != nil {
		return Connection{}, fmt.Errorf("create connection: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Connection{}, fmt.Errorf("create connection id: %w", err)
	}
	return s.ConnectionByID(ctx, id)
}

// UpdateConnection replaces the mutable fields of a connection. An empty
// Password keeps the stored secret; ClearPassword removes it.
func (s *Store) UpdateConnection(ctx context.Context, c Connection) (Connection, error) {
	if err := c.Validate(); err != nil {
		return Connection{}, err
	}
	sealed, err := s.sealPassword(c.Password)
	if err != nil {
		return Connection{}, err
	}
	sets := []string{
		`name = ?`, `host = ?`, `port = ?`, `client_id = ?`, `username = ?`,
		`tls = ?`, `tls_insecure = ?`, `ca_pem = ?`, `topic_filter = ?`,
		`qos = ?`, `enabled = ?`, `updated_at = ?`,
	}
	args := []any{
		c.Name, c.Host, c.Port, c.ClientID, c.Username,
		boolInt(c.TLS), boolInt(c.TLSInsecure), c.CAPEM, c.TopicFilter,
		c.QoS, boolInt(c.Enabled), time.Now().UnixMilli(),
	}
	switch {
	case c.ClearPassword:
		sets = append(sets, `password_enc = NULL`)
	case sealed != nil:
		sets = append(sets, `password_enc = ?`)
		args = append(args, sealed)
	}
	args = append(args, c.ID)
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return Connection{}, fmt.Errorf("update connection: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return Connection{}, ErrNotFound
	}
	return s.ConnectionByID(ctx, c.ID)
}

// DeleteConnection removes the connection and, by cascade, its topics and
// message history.
func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete connection: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Credentials returns the decrypted password for the broker client. An absent
// password yields an empty string, not an error.
func (s *Store) Credentials(ctx context.Context, id int64) (username, password string, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT username, password_enc FROM connections WHERE id = ?`, id).Scan(&username, &sealed)
	if err == sql.ErrNoRows {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("read credentials: %w", err)
	}
	if len(sealed) == 0 {
		return username, "", nil
	}
	plaintext, err := s.key.Open(sealed)
	if err != nil {
		return "", "", fmt.Errorf("decrypt password for connection %d: %w", id, err)
	}
	return username, string(plaintext), nil
}

func (s *Store) sealPassword(password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	sealed, err := s.key.Seal([]byte(password))
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}
	return sealed, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
