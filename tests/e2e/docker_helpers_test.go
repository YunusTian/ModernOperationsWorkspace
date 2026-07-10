// docker_helpers_test.go 是 Docker Plugin 真实 daemon E2E 的公共装配。
//
// 触发条件（默认整包跳过）：
//   - 环境变量 MOW_DOCKER_E2E=1 显式开启
//   - 且 DOCKER_HOST（或默认的 unix:///var/run/docker.sock）可以正常 ping
//     其中一个不满足即 t.Skip，避免开发者本机 CI 里被无关失败打断。
//
// 依赖：
//   - 一个可访问的 Docker daemon（Linux CI 用 services: docker 或宿主 daemon）
//   - `plugins/docker` 插件二进制。优先复用 MOW_DOCKER_PLUGIN 指向的预编译产物；
//     否则 TestMain 会用 `go build` 自动生成一份到临时目录。
//
// 与 SSH E2E 的关系：docker E2E 单独构建 plugMgr + engine，不复用 rig（rig
// 里内置了 SSH plugin 的 Register/Enable）。两条 pipeline 互相独立、可并行跑。
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/connection"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginclient"
)

// dockerPluginBinary 由 dockerBuildPluginOnce 惰性初始化。
var dockerPluginBinary string

// dockerRig 是 Docker E2E 专用装配。
type dockerRig struct {
	Engine  *command.Engine
	ConnMgr *connection.Manager
	PlugMgr *plugin.Manager
	Loaded  *pluginclient.LoadedPlugin

	TargetID string
	Host     string // 归一化后的 docker host，例："unix:///var/run/docker.sock"
}

// dockerE2EEnabled 报告是否满足运行 Docker E2E 的三个前置条件。
// 不满足时返回 (false, reason)，供调用方 t.Skip 用。
func dockerE2EEnabled() (bool, string) {
	if os.Getenv("MOW_DOCKER_E2E") != "1" {
		return false, "MOW_DOCKER_E2E!=1"
	}
	host := resolveDockerHost()
	if host == "" {
		return false, "no docker host env / unix socket detected"
	}
	// 主动 ping /_ping：daemon 可达才继续，否则 skip 而不是 fail。
	if err := pingDocker(host, 3*time.Second); err != nil {
		return false, "docker daemon not reachable: " + err.Error()
	}
	return true, host
}

// resolveDockerHost 优先看 DOCKER_HOST；否则回退到平台默认 unix socket。
// 当前 CI 只在 Linux 上跑真实 daemon E2E，Windows 上留待 v0.3.1（npipe 支持时）。
func resolveDockerHost() string {
	if h := strings.TrimSpace(os.Getenv("DOCKER_HOST")); h != "" {
		return h
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/var/run/docker.sock"); err == nil {
			return "unix:///var/run/docker.sock"
		}
	}
	return ""
}

// pingDocker 通过底层 net.Dial + /_ping 端点探活。
// 使用最小超时；只关心"能不能连上"，不关心具体版本。
func pingDocker(host string, timeout time.Duration) error {
	scheme, addr, err := splitDockerHost(host)
	if err != nil {
		return err
	}
	tr := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, scheme, addr)
		},
	}
	client := &http.Client{Transport: tr, Timeout: timeout}
	// scheme 为 tcp 时 URL 用真实 host；unix 时用占位。
	base := "http://docker"
	if scheme == "tcp" {
		base = "http://" + addr
	}
	resp, err := client.Get(base + "/_ping")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("ping status %d", resp.StatusCode)
	}
	return nil
}

// splitDockerHost 只解析 unix / tcp；npipe / tls 交由 v0.3.1 覆盖。
func splitDockerHost(host string) (string, string, error) {
	switch {
	case strings.HasPrefix(host, "unix://"):
		return "unix", strings.TrimPrefix(host, "unix://"), nil
	case strings.HasPrefix(host, "tcp://"):
		return "tcp", strings.TrimPrefix(host, "tcp://"), nil
	default:
		return "", "", fmt.Errorf("unsupported docker host %q for e2e", host)
	}
}

// dockerBuildPluginOnce 编译 plugins/docker（若外部未提供 MOW_DOCKER_PLUGIN）。
// 惰性执行；同一测试进程内多次调用只 build 一次。
func dockerBuildPluginOnce(t *testing.T) string {
	t.Helper()
	if dockerPluginBinary != "" {
		return dockerPluginBinary
	}
	if bin := os.Getenv("MOW_DOCKER_PLUGIN"); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("MOW_DOCKER_PLUGIN=%s not accessible: %v", bin, err)
		}
		dockerPluginBinary = bin
		return bin
	}

	dir, err := os.MkdirTemp("", "mow-docker-e2e-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	// 不 defer RemoveAll：pluginclient 需要在 rig teardown 之前存活；
	// Cleanup 里再删。
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	bin := filepath.Join(dir, "docker-plugin"+execSuffix())
	src, err := findModuleDir("../../plugins/docker")
	if err != nil {
		t.Fatalf("locate plugin src: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build docker plugin: %v", err)
	}
	dockerPluginBinary = bin
	return bin
}

// newDockerRig 装配 ConnMgr / PlugMgr / Engine 并 Enable docker 插件。
// 通过 t.Cleanup 保证 daemon 侧不残留任何测试容器（由具体用例负责 rm）。
func newDockerRig(t *testing.T, host string) *dockerRig {
	t.Helper()
	bin := dockerBuildPluginOnce(t)

	dataDir := t.TempDir()
	log := logger.Default()

	connMgr, err := connection.NewManager(connection.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("connection manager: %v", err)
	}
	plugMgr := plugin.NewManager(plugin.Options{Logger: log, DataDir: dataDir})
	engine := command.New(command.Options{
		Manager:  plugMgr,
		Logger:   log,
		Resolver: connMgr,
		// Confirm：dangerous rm 用例既要能"确认→通过"，也要能"不确认→拒绝"。
		// - AllowConfirmer 让 Engine 层不再主动拒绝未确认调用
		// - 插件层 dangerous.go 会独立校验 Confirmed=true（应用层护栏）
		// 于是拒绝路径由插件返回 CONFIRMATION_REQUIRED，允许路径由测试传 Confirmed=true。
		Confirm: command.AllowConfirmer{},
	})

	lp, err := pluginclient.LoadFromBinary(bin, nil)
	if err != nil {
		t.Fatalf("load docker plugin: %v", err)
	}
	if err := plugMgr.Register(lp.Plugin); err != nil {
		lp.Close()
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := plugMgr.Enable(ctx, "docker", sdk.InitRequest{DataDir: dataDir}); err != nil {
		lp.Close()
		t.Fatalf("enable docker plugin: %v", err)
	}

	targetID := "dk-e2e"
	if err := connMgr.Upsert(connection.Target{
		ID:   targetID,
		Type: connection.TypeDocker,
		Name: "e2e docker",
	}, &connection.DockerCredentials{Host: host}); err != nil {
		lp.Close()
		t.Fatalf("upsert docker target: %v", err)
	}

	r := &dockerRig{
		Engine:   engine,
		ConnMgr:  connMgr,
		PlugMgr:  plugMgr,
		Loaded:   lp,
		TargetID: targetID,
		Host:     host,
	}
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = plugMgr.Shutdown(shutdownCtx)
		lp.Close()
	})
	return r
}

// runDockerCommand 是"单次调用 + JSON 解码"的语法糖；仅用于非流式 Command。
// dst 可为 nil 表示不关心响应体。opts 可选 —— 用于覆盖 Confirmed / Timeout 等字段。
func (r *dockerRig) runDockerCommand(
	ctx context.Context, t *testing.T, commandID string, params any, dst any,
	opts ...func(*command.Request),
) (*command.Response, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	req := command.Request{
		PluginID:  "docker",
		CommandID: commandID,
		Params:    raw,
		TargetID:  r.TargetID,
		Caller:    sdk.Caller{Type: sdk.CallerCLI, User: "e2e"},
	}
	for _, opt := range opts {
		opt(&req)
	}
	resp, err := r.Engine.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	if dst != nil && len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, dst); err != nil {
			return resp, fmt.Errorf("decode %s data: %w (raw=%s)", commandID, err, string(resp.Data))
		}
	}
	return resp, nil
}

// withConfirmed 让 runDockerCommand 显式带 Confirmed=true，用于 docker.rm。
func withConfirmed() func(*command.Request) {
	return func(r *command.Request) { r.Confirmed = true }
}

// dockerContainerBrief 与 plugins/docker listResult 字段最小对齐。
// 只挑测试必要的字段，避免与插件内部演进耦合。
type dockerContainerBrief struct {
	ID    string   `json:"id"`
	Names []string `json:"names"`
	Image string   `json:"image"`
	State string   `json:"state"`
}

// -----------------------------------------------------------------------------
// rawEngine —— 直接和 Docker Engine HTTP API 说话
//
// 仅用于 E2E helper：
//   - 创建 / 启动 / 删除测试容器（v0.3 plugin 未提供 docker.create）
//   - 轮询容器状态（避免和插件流式接口耦合）
//
// 生产代码永远只走 Command Engine，禁止使用这个 helper。
// -----------------------------------------------------------------------------

type rawEngine struct {
	client *http.Client
	base   string
}

func newRawEngine(t *testing.T, host string) *rawEngine {
	t.Helper()
	scheme, addr, err := splitDockerHost(host)
	if err != nil {
		t.Fatalf("split docker host %q: %v", host, err)
	}
	tr := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, scheme, addr)
		},
	}
	base := "http://docker"
	if scheme == "tcp" {
		base = "http://" + addr
	}
	return &rawEngine{
		client: &http.Client{Transport: tr, Timeout: 60 * time.Second},
		base:   base,
	}
}

func (e *rawEngine) postJSON(path string, body any, dst any) error {
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reader = strings.NewReader(string(raw))
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequest("POST", e.base+path, reader)
	} else {
		req, err = http.NewRequest("POST", e.base+path, nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		return fmt.Errorf("engine POST %s status %d: %s", path, resp.StatusCode, string(buf[:n]))
	}
	if dst == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (e *rawEngine) getJSON(path string, dst any) error {
	resp, err := e.client.Get(e.base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("engine GET %s status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (e *rawEngine) deleteNoBody(path string) error {
	req, err := http.NewRequest("DELETE", e.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		return fmt.Errorf("engine DELETE %s status %d", path, resp.StatusCode)
	}
	return nil
}
