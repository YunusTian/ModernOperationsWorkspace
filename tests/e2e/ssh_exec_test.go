// Package e2e 是 MOW 的端到端测试。
//
// v0.1 的第一个用例：让 ssh.exec 真正跑通 stdout 回显。
//
//	Test flow:
//	  1. build plugins/ssh into a temp binary
//	  2. spin up an in-process SSH server (gliderlabs/ssh)
//	  3. register a Target pointing at the fake server via connection.Manager
//	  4. load the plugin via pluginclient, run ssh.exec through command.Engine
//	  5. assert stdout / exit_code
//
// 通过这条链路验证 grpcbridge 的 Connection 信封透传是"能用的"，
// 不只是"能编译"。
package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	glssh "github.com/gliderlabs/ssh"
	xssh "golang.org/x/crypto/ssh"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/connection"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginclient"
)

// -----------------------------------------------------------------------------
// TestMain：编译 SSH 插件到临时目录
// -----------------------------------------------------------------------------

var (
	pluginBinary string
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "mow-e2e-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "ssh-plugin"+execSuffix())
	// 通过 workspace 的相对路径定位 plugins/ssh 源码目录
	src, err := findModuleDir("../../plugins/ssh")
	if err != nil {
		return 0, err
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("build ssh plugin: %w", err)
	}
	pluginBinary = bin
	return m.Run(), nil
}

func execSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func findModuleDir(rel string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	p := filepath.Join(wd, rel)
	if _, err := os.Stat(filepath.Join(p, "go.mod")); err != nil {
		return "", fmt.Errorf("plugin source not found at %s: %w", p, err)
	}
	return p, nil
}

// -----------------------------------------------------------------------------
// In-process SSH server
// -----------------------------------------------------------------------------

type fakeSSHServer struct {
	addr   string
	port   int
	stop   func()
	execCh chan string // 每次执行的 command line
}

// startFakeSSHServer 启动一个只支持 "exec" 的 SSH server。
// 认证：仅接受给定 user + password。
// 行为：echo 用户 cmd（简单模拟），额外 stderr 输出一段固定文本以覆盖两路通道。
func startFakeSSHServer(t *testing.T, user, password string) *fakeSSHServer {
	t.Helper()

	// 生成一次性 host key
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	fs := &fakeSSHServer{execCh: make(chan string, 8)}

	handler := func(s glssh.Session) {
		cmdline := strings.Join(s.Command(), " ")
		select {
		case fs.execCh <- cmdline:
		default:
		}
		// 覆盖 stdout / stderr / exit code 三路
		_, _ = io.WriteString(s, "echo:"+cmdline+"\n")
		_, _ = io.WriteString(s.Stderr(), "warn line\n")
		_ = s.Exit(0)
	}

	server := &glssh.Server{
		Addr:    "127.0.0.1:0",
		Handler: handler,
		PasswordHandler: func(ctx glssh.Context, given string) bool {
			return ctx.User() == user && given == password
		},
	}
	server.AddHostKey(signer)

	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fs.addr = ln.Addr().String()
	fs.port = ln.Addr().(*net.TCPAddr).Port

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = server.Serve(ln)
	}()

	fs.stop = func() {
		_ = server.Close()
		wg.Wait()
	}
	return fs
}

// -----------------------------------------------------------------------------
// The actual e2e
// -----------------------------------------------------------------------------

func TestSSHExec_EndToEnd(t *testing.T) {
	if pluginBinary == "" {
		t.Fatal("plugin binary not built; check TestMain")
	}

	const (
		user     = "e2euser"
		password = "e2epass"
	)
	fs := startFakeSSHServer(t, user, password)
	defer fs.stop()

	dataDir := t.TempDir()

	// ---- Connection Manager ----
	connMgr, err := connection.NewManager(connection.Options{
		DataDir: dataDir,
	})
	if err != nil {
		t.Fatalf("connection manager: %v", err)
	}
	if err := connMgr.Upsert(connection.Target{
		ID:   "fake",
		Type: connection.TypeSSH,
		Host: "127.0.0.1",
		Port: fs.port,
		User: user,
	}, &connection.SSHCredentials{
		Method:         connection.SSHAuthPassword,
		Password:       password,
		KnownHostsMode: "insecure-ignore",
	}); err != nil {
		t.Fatalf("upsert target: %v", err)
	}

	// ---- Plugin Manager + Engine ----
	log := logger.Default()
	plugMgr := plugin.NewManager(plugin.Options{Logger: log, DataDir: dataDir})
	engine := command.New(command.Options{
		Manager: plugMgr,
		Logger:  log,
	})

	lp, err := pluginclient.LoadFromBinary(pluginBinary, nil)
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	defer lp.Close()

	if err := plugMgr.Register(lp.Plugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := plugMgr.Enable(ctx, "ssh", sdk.InitRequest{DataDir: dataDir}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	defer plugMgr.Shutdown(ctx)

	// ---- Open Connection & Run ssh.exec ----
	conn, err := connMgr.Open(ctx, "fake")
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	resp, err := engine.Run(ctx, command.Request{
		PluginID:   "ssh",
		CommandID:  "exec",
		Params:     json.RawMessage(`{"cmd":"uptime"}`),
		Connection: conn,
		Caller:     sdk.Caller{Type: sdk.CallerCLI, User: "test"},
		Timeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}

	// ---- Assert ----
	var out struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode data: %v (raw=%s)", err, string(resp.Data))
	}
	if want := "echo:uptime\n"; out.Stdout != want {
		t.Errorf("stdout mismatch\n want: %q\n got:  %q", want, out.Stdout)
	}
	if !strings.Contains(out.Stderr, "warn line") {
		t.Errorf("stderr should contain 'warn line', got %q", out.Stderr)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code want 0, got %d", out.ExitCode)
	}

	// 服务端也应观测到这条 exec
	select {
	case cmdline := <-fs.execCh:
		if cmdline != "uptime" {
			t.Errorf("server observed cmd %q, want %q", cmdline, "uptime")
		}
	case <-time.After(2 * time.Second):
		t.Error("server did not observe exec within 2s")
	}
}
