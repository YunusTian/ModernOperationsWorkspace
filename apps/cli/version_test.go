package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mow/mow/sdk/version"
)

func TestVersionCommand(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil { t.Fatal(err) }
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version output %q != %q", got, version.Version)
	}
}

