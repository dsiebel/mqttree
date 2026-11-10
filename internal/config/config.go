// Package config resolves the process configuration from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the immutable process configuration. Everything mutable at runtime
// (retention, payload caps) lives in the store's settings table instead.
type Config struct {
	Listen        string
	DataDir       string
	SecretKeyFile string
	LogLevel      string
}

// FromEnv reads the configuration, applying defaults for anything unset.
func FromEnv() (Config, error) {
	c := Config{
		Listen:        envOr("MQTTREE_LISTEN", "127.0.0.1:8080"),
		DataDir:       envOr("MQTTREE_DATA_DIR", "/var/lib/mqttree"),
		SecretKeyFile: envOr("MQTTREE_SECRET_KEY_FILE", "/etc/mqttree/secret.key"),
		LogLevel:      strings.ToLower(envOr("MQTTREE_LOG_LEVEL", "info")),
	}
	if c.DataDir == "" {
		return Config{}, fmt.Errorf("MQTTREE_DATA_DIR must not be empty")
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data dir: %w", err)
	}
	c.DataDir = abs
	return c, nil
}

// DBPath is the SQLite database file inside the data directory.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "mqttree.db") }

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
