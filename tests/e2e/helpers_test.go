package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

var pluginBinary string

func TestMain(m *testing.M) {
	code, err := buildPlugin(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func buildPlugin(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "mow-e2e-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "ssh-plugin"+execSuffix())
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
// Fake SSH server
// -----------------------------------------------------------------------------

// sessionHandler 是 fakeSSHServer 每次 exec 会话回调。
// 通过 t.Fatal 中断会导致测试 goroutine 崩溃——所以 handler 内不要用 require。
type sessionHandler func(s glssh.Session)

type fakeSSHServer struct {
	Addr string
	Port int
	// Conns 记录已建立的 TCP 连接数，供会话池复用测试使用。
	Conns atomic.Int32

	stop func()
}

// countingListener 记录 Accept 次数（每次 SSH 连接建立都会 Accept 一次）。
type countingListener struct {
	net.Listener
	count *atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.count.Add(1)
	}
	return c, err
}

// serverOption 用于按需配置 fakeSSHServer 的鉴权方式。
type serverOption func(*glssh.Server)

// withPassword 启用用户名 + 密码鉴权。
func withPassword(user, password string) serverOption {
	return func(s *glssh.Server) {
		s.PasswordHandler = func(ctx glssh.Context, given string) bool {
			return ctx.User() == user && given == password
		}
	}
}

// withPublicKey 启用公钥鉴权。authorized 是被授权的公钥。
func withPublicKey(user string, authorized xssh.PublicKey) serverOption {
	return func(s *glssh.Server) {
		s.PublicKeyHandler = func(ctx glssh.Context, given glssh.PublicKey) bool {
			if ctx.User() != user {
				return false
			}
			return glssh.KeysEqual(given, authorized)
		}
	}
}

// startFakeSSHServer 启动一个 in-process SSH server。
// handler 描述会话行为；opts 决定鉴权方式（可组合）。
func startFakeSSHServer(t *testing.T, h sessionHandler, opts ...serverOption) *fakeSSHServer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	fs := &fakeSSHServer{}

	server := &glssh.Server{
		Addr:    "127.0.0.1:0",
		Handler: glssh.Handler(h),
	}
	for _, opt := range opts {
		opt(server)
	}
	server.AddHostKey(signer)

	raw, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := &countingListener{Listener: raw, count: &fs.Conns}
	fs.Addr = raw.Addr().String()
	fs.Port = raw.Addr().(*net.TCPAddr).Port

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
	t.Cleanup(fs.stop)
	return fs
}

// -----------------------------------------------------------------------------
// Test rig：把 Engine / ConnMgr / PluginManager 装配成一个可运行环境
// -----------------------------------------------------------------------------

type rig struct {
	Engine  *command.Engine
	ConnMgr *connection.Manager
	PlugMgr *plugin.Manager
	Loaded  *pluginclient.LoadedPlugin
}

// newRig 装配 Core 依赖并注册 + Enable SSH 插件。
// 通过 t.Cleanup 保证测试结束时释放资源。
func newRig(t *testing.T) *rig {
	t.Helper()
	if pluginBinary == "" {
		t.Fatal("plugin binary not built; check TestMain")
	}

	dataDir := t.TempDir()
	log := logger.Default()

	connMgr, err := connection.NewManager(connection.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("connection manager: %v", err)
	}
	plugMgr := plugin.NewManager(plugin.Options{Logger: log, DataDir: dataDir})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})

	lp, err := pluginclient.LoadFromBinary(pluginBinary, nil)
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	if err := plugMgr.Register(lp.Plugin); err != nil {
		lp.Close()
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := plugMgr.Enable(ctx, "ssh", sdk.InitRequest{DataDir: dataDir}); err != nil {
		lp.Close()
		t.Fatalf("enable: %v", err)
	}

	r := &rig{Engine: engine, ConnMgr: connMgr, PlugMgr: plugMgr, Loaded: lp}
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = plugMgr.Shutdown(shutdownCtx)
		lp.Close()
	})
	return r
}

// upsertPasswordTarget 注册一个基于密码的 SSH Target 到 ConnMgr。
func (r *rig) upsertPasswordTarget(t *testing.T, id, host string, port int, user, password string) {
	t.Helper()
	if err := r.ConnMgr.Upsert(connection.Target{
		ID: id, Type: connection.TypeSSH,
		Host: host, Port: port, User: user,
	}, &connection.SSHCredentials{
		Method:         connection.SSHAuthPassword,
		Password:       password,
		KnownHostsMode: "insecure-ignore",
	}); err != nil {
		t.Fatalf("upsert target: %v", err)
	}
}

// upsertKeyTarget 注册一个基于私钥（PEM 字节）的 SSH Target。
func (r *rig) upsertKeyTarget(t *testing.T, id, host string, port int, user string, privateKeyPEM []byte) {
	t.Helper()
	if err := r.ConnMgr.Upsert(connection.Target{
		ID: id, Type: connection.TypeSSH,
		Host: host, Port: port, User: user,
	}, &connection.SSHCredentials{
		Method:         connection.SSHAuthPrivateKey,
		PrivateKey:     string(privateKeyPEM),
		KnownHostsMode: "insecure-ignore",
	}); err != nil {
		t.Fatalf("upsert target: %v", err)
	}
}

// generateEd25519KeyPair 生成一次性 ed25519 密钥对。
// 返回：PEM 编码的私钥字节（OpenSSH format）与 xssh.PublicKey。
func generateEd25519KeyPair(t *testing.T) ([]byte, xssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen ed25519: %v", err)
	}
	block, err := xssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(block)
	sshPub, err := xssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrap public key: %v", err)
	}
	return pemBytes, sshPub
}

// runExec 通过 Engine 执行一次 ssh.exec，并返回解析后的结果。
func (r *rig) runExec(ctx context.Context, t *testing.T, targetID string, params map[string]any, timeout time.Duration) (*execResult, *command.Response, error) {
	t.Helper()
	conn, err := r.ConnMgr.Open(ctx, targetID)
	if err != nil {
		return nil, nil, fmt.Errorf("open conn: %w", err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal params: %w", err)
	}
	resp, err := r.Engine.Run(ctx, command.Request{
		PluginID:   "ssh",
		CommandID:  "exec",
		Params:     raw,
		Connection: conn,
		Caller:     sdk.Caller{Type: sdk.CallerCLI, User: "test"},
		Timeout:    timeout,
	})
	if err != nil {
		return nil, nil, err
	}
	var out execResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, resp, fmt.Errorf("decode data: %w (raw=%s)", err, string(resp.Data))
	}
	return &out, resp, nil
}

// execResult 与 plugins/ssh 的输出契约保持一致。
type execResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// -----------------------------------------------------------------------------
// 会话 handler 工厂
// -----------------------------------------------------------------------------

// echoHandler 返回一个把 command line 回显到 stdout / 固定 stderr / exit=code 的 handler。
// observe（可选）在 handler 触发时收到 cmd line。
func echoHandler(exitCode int, observe chan<- string) sessionHandler {
	return func(s glssh.Session) {
		cmdline := strings.Join(s.Command(), " ")
		if observe != nil {
			select {
			case observe <- cmdline:
			default:
			}
		}
		_, _ = io.WriteString(s, "echo:"+cmdline+"\n")
		_, _ = io.WriteString(s.Stderr(), "warn line\n")
		_ = s.Exit(exitCode)
	}
}

// stdinEchoHandler 把 session.Stdin 完整回读到 stdout；exit=0。
func stdinEchoHandler() sessionHandler {
	return func(s glssh.Session) {
		_, _ = io.Copy(s, s)
		_ = s.Exit(0)
	}
}

// sleepHandler 阻塞在 ctx.Done 上（模拟长时间执行）；用于 cancel 测试。
func sleepHandler() sessionHandler {
	return func(s glssh.Session) {
		<-s.Context().Done()
		_ = s.Exit(255)
	}
}