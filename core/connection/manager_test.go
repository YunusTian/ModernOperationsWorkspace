package connection_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mow/mow/core/connection"
)

func newTestManager(t *testing.T) *connection.Manager {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	m, err := connection.NewManager(connection.Options{
		DataDir:  t.TempDir(),
		KeyStore: connection.StaticKeyStore{K: key},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m
}

func TestSealer_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	s := connection.NewSealer(connection.StaticKeyStore{K: key})
	plain := []byte("hello, world")
	sealed, err := s.Seal(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(sealed) <= len(plain) {
		t.Fatalf("sealed should be longer than plain (has nonce+tag), got %d vs %d", len(sealed), len(plain))
	}
	out, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(out) != string(plain) {
		t.Errorf("mismatch: %q", out)
	}
}

func TestSealer_TamperDetected(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	s := connection.NewSealer(connection.StaticKeyStore{K: key})
	sealed, err := s.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sealed[len(sealed)-1] ^= 0x01
	if _, err := s.Open(sealed); err == nil {
		t.Error("tampered ciphertext should fail to open")
	}
}

func TestFileKeyStore_Persists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "master.key")
	ks := connection.NewFileKeyStore(path)
	k1, err := ks.Key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	ks2 := connection.NewFileKeyStore(path)
	k2, err := ks2.Key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if len(k1) != 32 || string(k1) != string(k2) {
		t.Error("FileKeyStore should return the same key across instances")
	}
}

func TestManager_UpsertGetOpen(t *testing.T) {
	m := newTestManager(t)
	tg := connection.Target{
		ID:   "srv01",
		Type: connection.TypeSSH,
		Name: "server 01",
		Host: "10.0.0.1",
		Port: 22,
		User: "root",
	}
	creds := &connection.SSHCredentials{
		Method:   connection.SSHAuthPassword,
		Password: "s3cret",
	}
	if err := m.Upsert(tg, creds); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok := m.Get("srv01")
	if !ok {
		t.Fatal("Get should find srv01")
	}
	if len(got.EncryptedCredentials) == 0 {
		t.Error("credentials should be encrypted on disk")
	}

	conn, err := m.Open(context.Background(), "srv01")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if conn.Type != "ssh" || conn.ID != "srv01" {
		t.Errorf("unexpected connection: %+v", conn)
	}
	var out connection.SSHCredentials
	if err := json.Unmarshal(conn.Credentials, &out); err != nil {
		t.Fatalf("unmarshal creds: %v", err)
	}
	if out.Password != "s3cret" {
		t.Errorf("password mismatch: %q", out.Password)
	}
	if conn.Metadata["host"] != "10.0.0.1" || conn.Metadata["user"] != "root" {
		t.Errorf("metadata mismatch: %+v", conn.Metadata)
	}
}

func TestManager_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}

	m1, err := connection.NewManager(connection.Options{
		DataDir:  dir,
		KeyStore: connection.StaticKeyStore{K: key},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := m1.Upsert(connection.Target{
		ID: "h1", Type: connection.TypeSSH, Host: "1.1.1.1", User: "u",
	}, &connection.SSHCredentials{Method: connection.SSHAuthPassword, Password: "pw"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	m2, err := connection.NewManager(connection.Options{
		DataDir:  dir,
		KeyStore: connection.StaticKeyStore{K: key},
	})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	conn, err := m2.Open(context.Background(), "h1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var creds connection.SSHCredentials
	if err := json.Unmarshal(conn.Credentials, &creds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if creds.Password != "pw" {
		t.Errorf("password did not survive round-trip: %q", creds.Password)
	}
}

func TestManager_ValidationRejectsBad(t *testing.T) {
	m := newTestManager(t)
	err := m.Upsert(connection.Target{Type: connection.TypeSSH}, nil)
	if err == nil {
		t.Error("empty ID should be rejected")
	}
	err = m.Upsert(connection.Target{ID: "x", Type: connection.TypeSSH}, nil)
	if err == nil {
		t.Error("SSH target without host/user should be rejected")
	}
	err = m.Upsert(
		connection.Target{ID: "x", Type: connection.TypeSSH, Host: "h", User: "u"},
		&connection.SSHCredentials{Method: connection.SSHAuthPassword}, // no password
	)
	if err == nil {
		t.Error("empty password should be rejected")
	}
}

func TestManager_Delete(t *testing.T) {
	m := newTestManager(t)
	_ = m.Upsert(connection.Target{ID: "x", Type: connection.TypeSSH, Host: "h", User: "u"},
		&connection.SSHCredentials{Method: connection.SSHAuthPassword, Password: "p"})
	if err := m.Delete("x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m.Get("x"); ok {
		t.Error("target should be gone")
	}
	if err := m.Delete("x"); err == nil {
		t.Error("second delete should fail")
	}
}
