package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mow/mow/sdk"
)

// =============================================================================
// parseMode
// =============================================================================

func TestParseMode_Empty(t *testing.T) {
	m, err := parseMode("", 0o644)
	if err != nil {
		t.Fatalf("empty should use fallback: %v", err)
	}
	if m != 0o644 {
		t.Errorf("empty: want 0644, got %o", m)
	}
}

func TestParseMode_OctalShort(t *testing.T) {
	m, err := parseMode("755", 0)
	if err != nil {
		t.Fatalf("755 should be valid: %v", err)
	}
	if m != 0o755 {
		t.Errorf("755: want 0755, got %o", m)
	}
}

func TestParseMode_OctalWithZero(t *testing.T) {
	m, err := parseMode("0644", 0)
	if err != nil {
		t.Fatalf("0644 should be valid: %v", err)
	}
	if m != 0o644 {
		t.Errorf("0644: want 0644, got %o", m)
	}
}

func TestParseMode_Prefix0o(t *testing.T) {
	m, err := parseMode("0o600", 0)
	if err != nil {
		t.Fatalf("0o600 should be valid: %v", err)
	}
	if m != 0o600 {
		t.Errorf("0o600: want 0600, got %o", m)
	}
}

func TestParseMode_Prefix0O(t *testing.T) {
	m, err := parseMode("0O700", 0)
	if err != nil {
		t.Fatalf("0O700 should be valid: %v", err)
	}
	if m != 0o700 {
		t.Errorf("0O700: want 0700, got %o", m)
	}
}

func TestParseMode_EdgeZero(t *testing.T) {
	m, err := parseMode("0", 0o644)
	if err != nil {
		t.Fatalf("0 should be valid: %v", err)
	}
	if m != 0 {
		t.Errorf("0: want 0, got %o", m)
	}
}

func TestParseMode_NonOctal(t *testing.T) {
	_, err := parseMode("abc", 0)
	if err == nil {
		t.Fatal("non-octal should error")
	}
}

func TestParseMode_NonOctalDigit(t *testing.T) {
	_, err := parseMode("789", 0)
	if err == nil {
		t.Fatal("'789' contains non-octal digit 8, should error")
	}
}

func TestParseMode_Only0oPrefix(t *testing.T) {
	// "0o" 前缀后无数值 → 结果应为 0
	m, err := parseMode("0o", 0o644)
	if err != nil {
		t.Fatalf("'0o' should default to 0 (no digits): %v", err)
	}
	if m != 0 {
		t.Errorf("'0o': want 0, got %o", m)
	}
}

func TestParseMode_PermMasking(t *testing.T) {
	// 1777 之类的扩展位应被 os.ModePerm 截断
	m, err := parseMode("1777", 0)
	if err != nil {
		t.Fatalf("1777: %v", err)
	}
	if m != 0o777 {
		t.Errorf("1777 masked: want 0777, got %o", m)
	}
}

// =============================================================================
// decodeParams
// =============================================================================

func TestDecodeParams_Valid(t *testing.T) {
	var v struct{ Name string }
	err := decodeParams(json.RawMessage(`{"name":"test"}`), &v)
	if err != nil {
		t.Fatalf("decode valid: %v", err)
	}
	if v.Name != "test" {
		t.Errorf("Name want test, got %q", v.Name)
	}
}

func TestDecodeParams_Empty(t *testing.T) {
	var v struct{ Name string }
	err := decodeParams(json.RawMessage{}, &v)
	if err != nil {
		t.Fatalf("empty raw should be nil: %v", err)
	}
	if v.Name != "" {
		t.Errorf("Name should be empty, got %q", v.Name)
	}
}

func TestDecodeParams_InvalidJSON(t *testing.T) {
	var v struct{ Name string }
	err := decodeParams(json.RawMessage(`{bad}`), &v)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID, got %v", err)
	}
}

func TestDecodeParams_Nil(t *testing.T) {
	var v struct{ Name string }
	err := decodeParams(nil, &v)
	if err != nil {
		t.Fatalf("nil raw should be nil: %v", err)
	}
}

// =============================================================================
// sftp.list
// =============================================================================

func TestSftpList_Spec(t *testing.T) {
	cmd := &sftpListCmd{}
	spec := cmd.Spec()
	if spec.ID != "sftp.list" {
		t.Errorf("ID want sftp.list, got %q", spec.ID)
	}
	if spec.Permission != sdk.PermRead {
		t.Errorf("Permission want Read, got %v", spec.Permission)
	}
	if spec.ConnectionType != "ssh" {
		t.Errorf("ConnectionType want ssh, got %q", spec.ConnectionType)
	}
	if spec.Description == "" {
		t.Error("Description should not be empty")
	}
}

func TestSftpList_ExecuteStream_ReturnsNotSupported(t *testing.T) {
	cmd := &sftpListCmd{}
	err := cmd.ExecuteStream(context.Background(), nil)
	if !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", err)
	}
}

func TestSftpList_Execute_MissingPath(t *testing.T) {
	cmd := &sftpListCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for missing path, got %v", err)
	}
}

func TestSftpList_Execute_EmptyPath(t *testing.T) {
	cmd := &sftpListCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{"path":""}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for empty path, got %v", err)
	}
}

func TestSftpList_Execute_InvalidJSON(t *testing.T) {
	cmd := &sftpListCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{bad}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for bad JSON, got %v", err)
	}
}

// =============================================================================
// sftp.upload
// =============================================================================

func TestSftpUpload_Spec(t *testing.T) {
	cmd := &sftpUploadCmd{}
	spec := cmd.Spec()
	if spec.ID != "sftp.upload" {
		t.Errorf("ID want sftp.upload, got %q", spec.ID)
	}
	if spec.Permission != sdk.PermWrite {
		t.Errorf("Permission want Write, got %v", spec.Permission)
	}
	if spec.ConnectionType != "ssh" {
		t.Errorf("ConnectionType want ssh, got %q", spec.ConnectionType)
	}
	if spec.Description == "" {
		t.Error("Description should not be empty")
	}
}

func TestSftpUpload_ExecuteStream_ReturnsNotSupported(t *testing.T) {
	cmd := &sftpUploadCmd{}
	err := cmd.ExecuteStream(context.Background(), nil)
	if !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", err)
	}
}

func TestSftpUpload_Execute_MissingRemotePath(t *testing.T) {
	cmd := &sftpUploadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{"content_b64":"aGVsbG8="}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for missing remote_path, got %v", err)
	}
}

func TestSftpUpload_Execute_NoSource(t *testing.T) {
	cmd := &sftpUploadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{"remote_path":"/tmp/a"}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID (no source), got %v", err)
	}
	if err != nil {
		t.Logf("error message: %s", se.Message)
	}
}

func TestSftpUpload_Execute_BothSources(t *testing.T) {
	cmd := &sftpUploadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(
		`{"remote_path":"/tmp/a","local_path":"/tmp/x","content_b64":"aGVsbG8="}`,
	)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID (mutual exclusive), got %v", err)
	}
}

func TestSftpUpload_Execute_InvalidB64(t *testing.T) {
	cmd := &sftpUploadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(
		`{"remote_path":"/tmp/a","content_b64":"!!not-base64!!"}`,
	)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for bad base64, got %v", err)
	}
}

func TestSftpUpload_Execute_InvalidMode(t *testing.T) {
	cmd := &sftpUploadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(
		`{"remote_path":"/tmp/a","content_b64":"aGVsbG8=","mode":"abc"}`,
	)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for invalid mode, got %v", err)
	}
}

func TestSftpUpload_Execute_ValidMode(t *testing.T) {
	// 参数全部合法后会走到 openSFTP → pool 为 nil panic
	// 验证参数校验阶段不报错
	defer func() {
		if r := recover(); r != nil {
			t.Logf("expected panic after param validation (nil pool): %v", r)
		}
	}()

	cmd := &sftpUploadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(
		`{"remote_path":"/tmp/a","content_b64":"aGVsbG8=","mode":"0755"}`,
	)}
	_, err := cmd.Execute(context.Background(), req)
	// 没有 early-return PARAM_INVALID 说明参数校验全部通过
	var se *sdk.Error
	if errors.As(err, &se) && se.Code == "PARAM_INVALID" {
		t.Error("valid params should not be rejected")
	}
}

// =============================================================================
// sftp.download
// =============================================================================

func TestSftpDownload_Spec(t *testing.T) {
	cmd := &sftpDownloadCmd{}
	spec := cmd.Spec()
	if spec.ID != "sftp.download" {
		t.Errorf("ID want sftp.download, got %q", spec.ID)
	}
	if spec.Permission != sdk.PermRead {
		t.Errorf("Permission want Read, got %v", spec.Permission)
	}
	if spec.ConnectionType != "ssh" {
		t.Errorf("ConnectionType want ssh, got %q", spec.ConnectionType)
	}
	if spec.Description == "" {
		t.Error("Description should not be empty")
	}
}

func TestSftpDownload_ExecuteStream_ReturnsNotSupported(t *testing.T) {
	cmd := &sftpDownloadCmd{}
	err := cmd.ExecuteStream(context.Background(), nil)
	if !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", err)
	}
}

func TestSftpDownload_Execute_MissingRemotePath(t *testing.T) {
	cmd := &sftpDownloadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for missing remote_path, got %v", err)
	}
}

func TestSftpDownload_Execute_EmptyRemotePath(t *testing.T) {
	cmd := &sftpDownloadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{"remote_path":""}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for empty remote_path, got %v", err)
	}
}

func TestSftpDownload_Execute_InvalidMode(t *testing.T) {
	cmd := &sftpDownloadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(
		`{"remote_path":"/tmp/a","mode":"abc"}`,
	)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for invalid mode, got %v", err)
	}
}

func TestSftpDownload_Execute_InvalidJSON(t *testing.T) {
	cmd := &sftpDownloadCmd{}
	req := &sdk.ExecuteRequest{Params: json.RawMessage(`{bad}`)}
	_, err := cmd.Execute(context.Background(), req)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID for bad JSON, got %v", err)
	}
}
