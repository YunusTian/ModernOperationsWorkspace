package logger

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]string{
		"debug": "DEBUG",
		"info":  "INFO",
		"warn":  "WARN",
		"error": "ERROR",
		"":      "INFO",
		"xxx":   "INFO",
	}
	for in, want := range cases {
		if got := parseLevel(in).String(); got != want {
			t.Errorf("parseLevel(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestLoggerJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Options{Level: "debug", Format: FormatJSON, Output: &buf})
	l.WithComponent("plugin").Info("loaded", "id", "ssh")

	out := buf.String()
	if !strings.Contains(out, `"component":"plugin"`) {
		t.Errorf("missing component field: %s", out)
	}
	if !strings.Contains(out, `"msg":"loaded"`) {
		t.Errorf("missing msg field: %s", out)
	}
	if !strings.Contains(out, `"id":"ssh"`) {
		t.Errorf("missing id field: %s", out)
	}
}

func TestContextRoundTrip(t *testing.T) {
	l := Init(Options{Level: "info", Format: FormatJSON, Output: &bytes.Buffer{}})
	ctx := Into(context.Background(), l)
	if From(ctx) != l {
		t.Fatal("From did not return the injected logger")
	}
	if From(context.Background()) == nil {
		t.Fatal("From should fallback to default logger")
	}
}
