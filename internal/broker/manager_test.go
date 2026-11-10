package broker

import (
	"strings"
	"testing"

	"github.com/dsiebel/mqttree/internal/store"
)

func TestBrokerURL(t *testing.T) {
	cases := []struct {
		connection store.Connection
		want       string
	}{
		{store.Connection{Host: "192.168.154.50", Port: 1883}, "mqtt://192.168.154.50:1883"},
		{store.Connection{Host: "broker.lan", Port: 8883, TLS: true}, "tls://broker.lan:8883"},
	}
	for _, testCase := range cases {
		parsed, err := brokerURL(testCase.connection)
		if err != nil {
			t.Fatalf("brokerURL: %v", err)
		}
		if parsed.String() != testCase.want {
			t.Errorf("url = %q, want %q", parsed.String(), testCase.want)
		}
	}
}

func TestTLSConfig(t *testing.T) {
	if config, err := tlsConfigFor(store.Connection{Host: "h", Port: 1883}); err != nil || config != nil {
		t.Fatalf("plain connection = %v, %v", config, err)
	}
	if _, err := tlsConfigFor(store.Connection{Host: "h", Port: 1883, CAPEM: "not a cert"}); err == nil {
		t.Error("expected an error for a CA without TLS")
	}
	if _, err := tlsConfigFor(store.Connection{Host: "h", Port: 8883, TLS: true, CAPEM: "not a cert"}); err == nil ||
		!strings.Contains(err.Error(), "valid certificate") {
		t.Errorf("invalid CA = %v", err)
	}
	config, err := tlsConfigFor(store.Connection{Host: "h", Port: 8883, TLS: true, TLSInsecure: true})
	if err != nil {
		t.Fatalf("insecure tls: %v", err)
	}
	if !config.InsecureSkipVerify {
		t.Error("InsecureSkipVerify not applied")
	}
}
