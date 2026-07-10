package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
)

func TestSplitHost(t *testing.T) {
	tests := []struct {
		host       string
		wantScheme string
		wantAddr   string
		wantErr    bool
	}{
		{"unix:///var/run/docker.sock", "unix", "/var/run/docker.sock", false},
		{"tcp://192.168.1.10:2375", "tcp", "192.168.1.10:2375", false},
		{"tcp://[::1]:2375", "tcp", "[::1]:2375", false},
		{"npipe:////./pipe/docker_engine", "npipe", "//./pipe/docker_engine", false},
		{"http://x:2375", "", "", true},
		{"tcp://", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			s, a, err := splitHost(tt.host)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if s != tt.wantScheme || a != tt.wantAddr {
				t.Fatalf("got (%q,%q), want (%q,%q)", s, a, tt.wantScheme, tt.wantAddr)
			}
		})
	}
}

func TestNormalizeAPIVersion(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"1.44":   "/v1.44",
		"v1.44":  "/v1.44",
		" 1.44 ": "/v1.44",
	}
	for in, want := range cases {
		if got := normalizeAPIVersion(in); got != want {
			t.Errorf("normalizeAPIVersion(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	creds, _ := json.Marshal(dockerCredentials{
		Host:       "tcp://127.0.0.1:2375",
		APIVersion: "1.44",
	})
	conn := &sdk.Connection{
		Type:        "docker",
		Credentials: creds,
	}
	dt, err := resolveTarget(conn)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if dt.Scheme != "tcp" || dt.NetAddr != "127.0.0.1:2375" {
		t.Fatalf("scheme/addr mismatch: %+v", dt)
	}
	if dt.APIVersion != "/v1.44" {
		t.Fatalf("api version = %q", dt.APIVersion)
	}
}

func TestResolveTarget_WrongType(t *testing.T) {
	_, err := resolveTarget(&sdk.Connection{Type: "ssh"})
	if err == nil {
		t.Fatal("expected error for non-docker type")
	}
}

func TestResolveTarget_EmptyHost(t *testing.T) {
	raw, _ := json.Marshal(dockerCredentials{})
	_, err := resolveTarget(&sdk.Connection{Type: "docker", Credentials: raw})
	if err == nil || !strings.Contains(err.Error(), "host is empty") {
		t.Fatalf("expected host empty error, got %v", err)
	}
}
