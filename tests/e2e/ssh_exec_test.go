// Package e2e 是 MOW 的端到端测试。
//
// 用例矩阵（v0.1）：
//
//	TestSSHExec_EndToEnd         正常路径（stdout / stderr / exit_code 全绿）
//	TestSSHExec_ExitCodeNonZero  远端 exit != 0 时不误报 error，透传退出码
//	TestSSHExec_Stdin            走 params.stdin，服务端 echo 回来验证
//	TestSSHExec_ContextCancel    短超时命中，返回 CANCELED
//	TestSessionPool_Reuse        连续两次 exec 只做一次 TCP 握手
//	TestSSHExec_PublicKey        ed25519 私钥鉴权，覆盖 privatekey 分支
//	TestSSHExec_MissingCmd       缺 cmd 参数应返回 PARAM_INVALID
//
// 所有用例共享 helpers_test.go 中的 rig / fakeSSHServer 装配代码。
package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 正常路径
// -----------------------------------------------------------------------------

func TestSSHExec_EndToEnd(t *testing.T) {
	const user, password = "e2euser", "e2epass"
	observed := make(chan string, 1)
	fs := startFakeSSHServer(t, echoHandler(0, observed), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "fake", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "uptime"}, 5*time.Second)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if want := "echo:uptime\n"; out.Stdout != want {
		t.Errorf("stdout mismatch: want %q got %q", want, out.Stdout)
	}
	if !strings.Contains(out.Stderr, "warn line") {
		t.Errorf("stderr should contain 'warn line', got %q", out.Stderr)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code want 0, got %d", out.ExitCode)
	}

	select {
	case cmdline := <-observed:
		if cmdline != "uptime" {
			t.Errorf("server observed %q, want %q", cmdline, "uptime")
		}
	case <-time.After(2 * time.Second):
		t.Error("server did not observe exec within 2s")
	}
}

// -----------------------------------------------------------------------------
// 远端非零 exit：不视为 error，透传 exit_code
// -----------------------------------------------------------------------------

func TestSSHExec_ExitCodeNonZero(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, echoHandler(2, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "fake", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "false"}, 5*time.Second)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if out.ExitCode != 2 {
		t.Errorf("exit_code want 2, got %d", out.ExitCode)
	}
	if want := "echo:false\n"; out.Stdout != want {
		t.Errorf("stdout mismatch: want %q got %q", want, out.Stdout)
	}
}

// -----------------------------------------------------------------------------
// stdin：把 params.stdin 灌入远端会话，服务端 echo 回读
// -----------------------------------------------------------------------------

func TestSSHExec_Stdin(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, stdinEchoHandler(), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "fake", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := "hello\nworld\n"
	out, _, err := r.runExec(ctx, t, "fake", map[string]any{
		"cmd":   ":", // 内容不重要，服务端只 io.Copy
		"stdin": payload,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if out.Stdout != payload {
		t.Errorf("stdout should echo stdin\n want: %q\n got:  %q", payload, out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code want 0, got %d", out.ExitCode)
	}
}

// -----------------------------------------------------------------------------
// context 取消：服务端 sleep，客户端在 timeout 内取消
// -----------------------------------------------------------------------------

func TestSSHExec_ContextCancel(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, sleepHandler(), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "fake", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "sleep 60"}, 500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on timeout, got nil")
	}
	if elapsed > 3*time.Second {
		t.Errorf("cancel took too long: %v", elapsed)
	}
	// 期望 sdk.Error{Code:"CANCELED"} 或 context.DeadlineExceeded
	var se *sdk.Error
	if errors.As(err, &se) {
		if se.Code != "CANCELED" && se.Code != "SSH_EXEC_FAILED" {
			t.Errorf("unexpected error code %q: %v", se.Code, err)
		}
	}
}

// -----------------------------------------------------------------------------
// SessionPool 复用：连续两次 exec，只应触发一次 TCP Accept
// -----------------------------------------------------------------------------

func TestSessionPool_Reuse(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "fake", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < 2; i++ {
		if _, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "hello"}, 5*time.Second); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	if got := fs.Conns.Load(); got != 1 {
		t.Errorf("expected 1 accepted TCP connection (pool reuse), got %d", got)
	}
}

// -----------------------------------------------------------------------------
// 公钥鉴权：ed25519 私钥 → 服务端 KeysEqual 比对
// -----------------------------------------------------------------------------

func TestSSHExec_PublicKey(t *testing.T) {
	const user = "keyuser"
	privatePEM, pub := generateEd25519KeyPair(t)
	fs := startFakeSSHServer(t, echoHandler(0, nil), withPublicKey(user, pub))

	r := newRig(t)
	r.upsertKeyTarget(t, "fake", "127.0.0.1", fs.Port, user, privatePEM)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "whoami"}, 5*time.Second)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if want := "echo:whoami\n"; out.Stdout != want {
		t.Errorf("stdout mismatch: want %q got %q", want, out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code want 0, got %d", out.ExitCode)
	}
}

// TestSSHExec_KeyPassphrase 覆盖带口令私钥的解密分支。
// 私钥用 passphrase 加密；插件端应能正确解密并完成鉴权。
func TestSSHExec_KeyPassphrase(t *testing.T) {
	const (
		user       = "keyuser"
		passphrase = "hunter2"
	)
	privatePEM, pub := generateEd25519KeyPairWithPassphrase(t, passphrase)
	fs := startFakeSSHServer(t, echoHandler(0, nil), withPublicKey(user, pub))

	r := newRig(t)
	r.upsertEncryptedKeyTarget(t, "fake", "127.0.0.1", fs.Port, user, privatePEM, passphrase)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "id"}, 5*time.Second)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if want := "echo:id\n"; out.Stdout != want {
		t.Errorf("stdout mismatch: want %q got %q", want, out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code want 0, got %d", out.ExitCode)
	}
}

// -----------------------------------------------------------------------------
// 参数校验：缺 cmd 应返回 sdk.Error{Code:"PARAM_INVALID"}，不接触远端
// -----------------------------------------------------------------------------

func TestSSHExec_MissingCmd(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "fake", "127.0.0.1", fs.Port, user, password)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := r.runExec(ctx, t, "fake", map[string]any{}, 5*time.Second)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected sdk.Error, got %T: %v", err, err)
	}
	if se.Code != "PARAM_INVALID" {
		t.Errorf("expected code PARAM_INVALID, got %q (%v)", se.Code, err)
	}
	// 参数校验发生在插件层，但应短路：远端不应观察到任何连接。
	if got := fs.Conns.Load(); got != 0 {
		t.Errorf("PARAM_INVALID should short-circuit before dial; got %d conns", got)
	}
}

// -----------------------------------------------------------------------------
// known_hosts accept-new：首次连接自动追写 known_hosts；第二次走 strict 命中
// -----------------------------------------------------------------------------

func TestSSHExec_AcceptNewAppendsKnownHosts(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	r.upsertAcceptNewTarget(t, "fake", "127.0.0.1", fs.Port, user, password, khPath)

	// 首次：文件不存在，callback 应追写
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "hello"}, 5*time.Second); err != nil {
		t.Fatalf("first run: %v", err)
	}
	data, err := os.ReadFile(khPath)
	if err != nil {
		t.Fatalf("known_hosts should be written: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("known_hosts should be non-empty after first exec")
	}
	firstSize := len(data)

	// 第二次：文件已存在，callback 走 strict，不应再追写
	if _, _, err := r.runExec(ctx, t, "fake", map[string]any{"cmd": "hello"}, 5*time.Second); err != nil {
		t.Fatalf("second run: %v", err)
	}
	data2, err := os.ReadFile(khPath)
	if err != nil {
		t.Fatalf("known_hosts should still exist: %v", err)
	}
	if len(data2) != firstSize {
		t.Errorf("known_hosts should not grow on second run: before=%d after=%d", firstSize, len(data2))
	}
}
