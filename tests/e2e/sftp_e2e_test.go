package e2e

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// SFTP List — 列出目录
// -----------------------------------------------------------------------------

func TestSFTPList_RootAfterUpload(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 先上传一个文件到根目录
	content := base64.StdEncoding.EncodeToString([]byte("hello"))
	_, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/test.txt",
		"content_b64": content,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 列出根目录
	list, err := r.runSFTPList(ctx, t, "srv", "/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Path != "/" {
		t.Errorf("path want /, got %q", list.Path)
	}
	found := false
	for _, e := range list.Entries {
		if e.Name == "test.txt" {
			found = true
			if e.IsDir {
				t.Error("test.txt should not be a directory")
			}
			if e.Size != 5 {
				t.Errorf("test.txt size want 5, got %d", e.Size)
			}
			break
		}
	}
	if !found {
		t.Errorf("test.txt not found in root listing: %+v", list.Entries)
	}
}

func TestSFTPList_EmptyDir(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 根目录应存在但没有自定义文件
	list, err := r.runSFTPList(ctx, t, "srv", "/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Path != "/" {
		t.Errorf("path want /, got %q", list.Path)
	}
	// InMemHandler 的 "/" 始终存在，entries 至少为空数组
	if list.Entries == nil {
		t.Error("entries should not be nil")
	}
}

func TestSFTPList_PathNotFound(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := r.runSFTPList(ctx, t, "srv", "/nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *sdk.Error, got %T: %v", err, err)
	}
	if se.Code != "SFTP_LIST_FAILED" {
		t.Errorf("expected SFTP_LIST_FAILED, got %q", se.Code)
	}
}

// -----------------------------------------------------------------------------
// SFTP Upload — content_b64
// -----------------------------------------------------------------------------

func TestSFTPUpload_ContentB64(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	payload := "Hello, SFTP E2E!"
	b64 := base64.StdEncoding.EncodeToString([]byte(payload))
	result, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/upload.txt",
		"content_b64": b64,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if result.RemotePath != "/upload.txt" {
		t.Errorf("remote_path want /upload.txt, got %q", result.RemotePath)
	}
	if result.BytesSent != int64(len(payload)) {
		t.Errorf("bytes_sent want %d, got %d", len(payload), result.BytesSent)
	}

	// 下载验证内容
	dl, err := r.runSFTPDownload(ctx, t, "srv", map[string]any{
		"remote_path": "/upload.txt",
	})
	if err != nil {
		t.Fatalf("download verify: %v", err)
	}
	if dl.BytesReceived != int64(len(payload)) {
		t.Errorf("bytes_received want %d, got %d", len(payload), dl.BytesReceived)
	}
	decoded, err := base64.StdEncoding.DecodeString(dl.ContentB64)
	if err != nil {
		t.Fatalf("decode downloaded b64: %v", err)
	}
	if string(decoded) != payload {
		t.Errorf("content mismatch: want %q, got %q", payload, string(decoded))
	}
}

func TestSFTPUpload_MkdirAll(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	b64 := base64.StdEncoding.EncodeToString([]byte("nested"))
	_, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/a/b/c/file.txt",
		"content_b64": b64,
		"mkdir_all":   true,
	})
	if err != nil {
		t.Fatalf("upload with mkdir_all: %v", err)
	}

	// 验证父目录存在
	list, err := r.runSFTPList(ctx, t, "srv", "/a/b/c")
	if err != nil {
		t.Fatalf("list nested dir: %v", err)
	}
	found := false
	for _, e := range list.Entries {
		if e.Name == "file.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("file.txt not found in /a/b/c")
	}
}

func TestSFTPUpload_OverwriteFalse_Exists(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	b64 := base64.StdEncoding.EncodeToString([]byte("first"))
	// 第一次上传
	_, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/existing.txt",
		"content_b64": b64,
	})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}

	// 第二次上传 overwrite=false
	f := false
	_, err = r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/existing.txt",
		"content_b64": b64,
		"overwrite":   &f,
	})
	if err == nil {
		t.Fatal("expected SFTP_EXISTS error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "SFTP_EXISTS" {
		t.Errorf("expected SFTP_EXISTS, got %v", err)
	}
}

func TestSFTPUpload_InvalidB64(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/bad.txt",
		"content_b64": "!!!not-valid-base64!!!",
	})
	if err == nil {
		t.Fatal("expected PARAM_INVALID")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SFTP Download — inline (base64)
// -----------------------------------------------------------------------------

func TestSFTPDownload_FileNotFound(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := r.runSFTPDownload(ctx, t, "srv", map[string]any{
		"remote_path": "/no-such-file",
	})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *sdk.Error, got %T: %v", err, err)
	}
	if se.Code != "SFTP_OPEN_REMOTE_FAILED" {
		t.Errorf("expected SFTP_OPEN_REMOTE_FAILED, got %q", se.Code)
	}
}

func TestSFTPDownload_ToLocalFile(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 先上传
	payload := "Download to file test!"
	b64 := base64.StdEncoding.EncodeToString([]byte(payload))
	_, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
		"remote_path": "/to_dl.txt",
		"content_b64": b64,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 下载到本地文件
	localPath := filepath.Join(t.TempDir(), "downloaded.txt")
	dl, err := r.runSFTPDownload(ctx, t, "srv", map[string]any{
		"remote_path": "/to_dl.txt",
		"local_path":  localPath,
	})
	if err != nil {
		t.Fatalf("download to file: %v", err)
	}
	if dl.BytesReceived != int64(len(payload)) {
		t.Errorf("bytes_received want %d, got %d", len(payload), dl.BytesReceived)
	}
	if dl.LocalPath != localPath {
		t.Errorf("local_path want %q, got %q", localPath, dl.LocalPath)
	}
	// 读取本地文件内容验证
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read local file: %v", err)
	}
	if string(data) != payload {
		t.Errorf("local file content mismatch: want %q, got %q", payload, string(data))
	}
}

// -----------------------------------------------------------------------------
// SFTP 全链路：list → upload → download → list 验证
// -----------------------------------------------------------------------------

func TestSFTP_FullRoundTrip(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServerWithSFTP(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. 初始 / 目录为空
	list, err := r.runSFTPList(ctx, t, "srv", "/")
	if err != nil {
		t.Fatalf("initial list: %v", err)
	}
	if len(list.Entries) != 0 {
		t.Errorf("root should be empty, got %d entries", len(list.Entries))
	}

	// 2. 上传两个文件
	for _, name := range []string{"a.txt", "b.txt"} {
		b64 := base64.StdEncoding.EncodeToString([]byte(name))
		_, err := r.runSFTPUpload(ctx, t, "srv", map[string]any{
			"remote_path": "/" + name,
			"content_b64": b64,
		})
		if err != nil {
			t.Fatalf("upload %s: %v", name, err)
		}
	}

	// 3. 列出根目录，验证两个文件
	list, err = r.runSFTPList(ctx, t, "srv", "/")
	if err != nil {
		t.Fatalf("list after upload: %v", err)
	}
	if len(list.Entries) != 2 {
		t.Errorf("want 2 entries, got %d: %+v", len(list.Entries), list.Entries)
	}

	// 4. 下载 a.txt 内联验证
	dl, err := r.runSFTPDownload(ctx, t, "srv", map[string]any{
		"remote_path": "/a.txt",
	})
	if err != nil {
		t.Fatalf("download a.txt: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(dl.ContentB64)
	if string(decoded) != "a.txt" {
		t.Errorf("a.txt content mismatch: got %q", string(decoded))
	}
}
