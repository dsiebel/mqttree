package store

import (
	"errors"
	"fmt"
)

// Validation bounds for the operator-tunable settings. They exist to keep a
// typo from turning into either unbounded growth or an empty UI.
const (
	minRetentionHours = 1
	maxRetentionHours = 24 * 365

	minMaxDBBytes = 16 << 20
	maxMaxDBBytes = 64 << 30

	minMaxPayloadBytes = 1 << 10
	maxMaxPayloadBytes = 4 << 20
)

// Settings are the operator-tunable knobs persisted in the database so that a
// running instance converges without a restart.
type Settings struct {
	// RetentionHours is how long a message is kept when the broker provides no
	// MQTT v5 message expiry interval.
	RetentionHours int `json:"retention_hours"`
	// MaxDBBytes caps the live database size; the oldest messages are pruned
	// first when the cap is exceeded.
	MaxDBBytes int64 `json:"max_db_bytes"`
	// MaxPayloadBytes truncates stored payloads. The UI flags truncated rows.
	MaxPayloadBytes int `json:"max_payload_bytes"`
}

// DefaultSettings is the first-boot configuration: a day of history, a 1 GiB
// cap and 256 KiB payloads.
func DefaultSettings() Settings {
	return Settings{
		RetentionHours:  24,
		MaxDBBytes:      1 << 30,
		MaxPayloadBytes: 256 << 10,
	}
}

// Normalize clamps a settings value into the allowed range.
func (s Settings) Normalize() (Settings, error) {
	if s.RetentionHours == 0 {
		s.RetentionHours = DefaultSettings().RetentionHours
	}
	if s.MaxDBBytes == 0 {
		s.MaxDBBytes = DefaultSettings().MaxDBBytes
	}
	if s.MaxPayloadBytes == 0 {
		s.MaxPayloadBytes = DefaultSettings().MaxPayloadBytes
	}
	if s.RetentionHours < minRetentionHours || s.RetentionHours > maxRetentionHours {
		return Settings{}, fmt.Errorf("retention_hours must be between %d and %d", minRetentionHours, maxRetentionHours)
	}
	if s.MaxDBBytes < minMaxDBBytes || s.MaxDBBytes > maxMaxDBBytes {
		return Settings{}, fmt.Errorf("max_db_bytes must be between %d and %d", minMaxDBBytes, maxMaxDBBytes)
	}
	if s.MaxPayloadBytes < minMaxPayloadBytes || s.MaxPayloadBytes > maxMaxPayloadBytes {
		return Settings{}, fmt.Errorf("max_payload_bytes must be between %d and %d", minMaxPayloadBytes, maxMaxPayloadBytes)
	}
	return s, nil
}

// Retained returns the retention window in milliseconds.
func (s Settings) Retained() int64 { return int64(s.RetentionHours) * 60 * 60 * 1000 }

// ErrNotFound is returned when a connection does not exist.
var ErrNotFound = errors.New("not found")
