package payload

import "testing"

func TestRender(t *testing.T) {
	cases := []struct {
		name     string
		input    []byte
		wantText string
	}{
		{"json", []byte(`{"temperature":21.5}`), `{"temperature":21.5}`},
		{"plain", []byte("online"), "online"},
		{"empty", []byte{}, ""},
		{"binary", []byte{0x00, 0x01, 0xff}, ""},
		{"control chars", []byte{'a', 0x07, 'b'}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, text := Render(testCase.input)
			if text != testCase.wantText {
				t.Errorf("text = %q, want %q", text, testCase.wantText)
			}
			if encoded == "" && len(testCase.input) > 0 {
				t.Error("empty base64 for a non-empty input")
			}
		})
	}
}
