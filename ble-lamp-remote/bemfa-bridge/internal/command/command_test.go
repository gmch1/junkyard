package command

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSupportedCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    Command
	}{
		{name: "on", payload: "on", want: Command{Payload: "on", Path: "/v1/light/on"}},
		{name: "off", payload: "off", want: Command{Payload: "off", Path: "/v1/light/off"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse([]byte(tt.payload))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Parse() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseRejectsUnsupportedCommands(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{"", "ON", " on ", "on\r\n", "on#80", "off#1", "toggle", strings.Repeat("x", 65)} {
		_, err := Parse([]byte(payload))
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Parse(%q) error = %v, want ErrUnsupported", payload, err)
		}
	}
}
