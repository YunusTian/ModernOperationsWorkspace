package main

import (
	"testing"

	"github.com/mow/mow/sdk/version"
)

func TestAppVersion(t *testing.T) {
	if got := (&App{}).Version(); got != version.Version {
		t.Fatalf("Version()=%q want %q", got, version.Version)
	}
}

