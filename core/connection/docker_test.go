package connection_test

import (
	"context"
	"testing"

	"github.com/mow/mow/core/connection"
)

func TestDockerCredentials_Validate(t *testing.T) {
	tests := []struct {
		name    string
		creds   connection.DockerCredentials
		wantErr bool
	}{
		{"unix ok", connection.DockerCredentials{Host: "unix:///var/run/docker.sock"}, false},
		{"tcp ok", connection.DockerCredentials{Host: "tcp://127.0.0.1:2375"}, false},
		{"npipe ok", connection.DockerCredentials{Host: "npipe:////./pipe/docker_engine"}, false},
		{"empty host", connection.DockerCredentials{}, true},
		{"bad scheme", connection.DockerCredentials{Host: "http://x:2375"}, true},
		{"partial tls", connection.DockerCredentials{Host: "tcp://x:2376", TLSCert: "c"}, true},
		{"full tls", connection.DockerCredentials{
			Host: "tcp://x:2376", TLSCA: "ca", TLSCert: "c", TLSKey: "k",
		}, false},
		{"tls verify no material", connection.DockerCredentials{Host: "tcp://x:2376", TLSVerify: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.creds.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestUpsertDockerTarget(t *testing.T) {
	m := newTestManager(t)
	tgt := connection.Target{
		ID:   "dk-local",
		Type: connection.TypeDocker,
		Name: "local docker",
	}
	creds := &connection.DockerCredentials{Host: "unix:///var/run/docker.sock"}
	if err := m.Upsert(tgt, creds); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, ok := m.Get("dk-local")
	if !ok {
		t.Fatal("target not found")
	}
	if got.Type != connection.TypeDocker {
		t.Fatalf("type = %q, want docker", got.Type)
	}

	conn, err := m.Open(context.Background(), "dk-local")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if conn.Type != "docker" {
		t.Fatalf("conn.Type = %q", conn.Type)
	}
	if len(conn.Credentials) == 0 {
		t.Fatal("credentials empty")
	}
}
