// Package payload turns raw MQTT payloads into something a browser can render
// without guessing: base64 always, plus decoded text when the bytes are valid
// UTF-8 and contain no control characters.
package payload

import (
	"encoding/base64"
	"unicode/utf8"
)

// Render returns the base64 form and, when the payload is human readable, the
// decoded text.
func Render(b []byte) (encoded, text string) {
	encoded = base64.StdEncoding.EncodeToString(b)
	if text, ok := asText(b); ok {
		return encoded, text
	}
	return encoded, ""
}

func asText(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", true
	}
	if !utf8.Valid(b) {
		return "", false
	}
	for _, r := range string(b) {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return string(b), true
}
