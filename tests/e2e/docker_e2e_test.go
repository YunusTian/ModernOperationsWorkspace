// docker_e2e_test.go —— Docker Plugin 真实 daemon 端到端测试。
//
// 触发：仅当 dockerE2EEnabled() 返回 true 时执行；否则全部 Skip。
//
// 覆盖范围（对齐 v0.3.1 计划中"真实 Docker Engine E2E"）：
//   - docker.list：注册容器后能被列出
//   - lifecycle：start / stop / restart（含 already_in_state 语义）
//   - docker.logs：stdout 流可读出到内存 sink
//   - docker.pull：拉取一个极小镜像并收到 progress event
//   - docker.exec：非 TTY 模式跑一次 `echo`，收到 stdout 与 exit_code=0
//   - docker.rm：未 confirmed → 拒绝；confirmed=true → 成功删除
//
// 命名约定：所有测试容器名都以 mow-e2e- 打头，方便 CI 出错时人工清理。
package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// 极小镜像（<2MB），用来跑 pull / lifecycle / exec 全套。
// 之所以选 alpine 而不是 busybox，是因为大多数 Linux CI 主机已经预拉了它，
// 拉取会走 daemon 缓存；即便冷启动也只有约 3MB。
const dockerE2EImage = "alpine:3.19"

// -----------------------------------------------------------------------------
// docker.list
// -----------------------------------------------------------------------------

func TestDockerE2E_List(t *testing.T) {
	ok, hostOrReason := dockerE2EEnabled()
	if !ok {
		t.Skipf("docker e2e skipped: %s", hostOrReason)
	}
	r := newDockerRig(t, hostOrReason)

	name := uniqueContainerName("list")
	ensureImage(t, r, dockerE2EImage)
	id := createLongRunning(t, r, name, dockerE2EImage)
	t.Cleanup(func() { forceRemove(t, r, id) })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var list struct {
		Containers []dockerContainerBrief `json:"containers"`
	}
	if _, err := r.runDockerCommand(ctx, t, "list",
		map[string]any{"all": true}, &list); err != nil {
		t.Fatalf("docker.list: %v", err)
	}
	if !containsContainer(list.Containers, id, name) {
		t.Fatalf("docker.list did not include %s (%s); got=%+v", name, id, list.Containers)
	}
}

// -----------------------------------------------------------------------------
// lifecycle: start / stop / restart（含 already_in_state）
// -----------------------------------------------------------------------------

func TestDockerE2E_Lifecycle(t *testing.T) {
	ok, hostOrReason := dockerE2EEnabled()
	if !ok {
		t.Skipf("docker e2e skipped: %s", hostOrReason)
	}
	r := newDockerRig(t, hostOrReason)

	name := uniqueContainerName("lifecycle")
	ensureImage(t, r, dockerE2EImage)
	id := createLongRunning(t, r, name, dockerE2EImage)
	t.Cleanup(func() { forceRemove(t, r, id) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 容器创建时是 running：第一次 start 应该返回 already_in_state=true / DOCKER_NOT_MODIFIED。
	if _, err := r.runDockerCommand(ctx, t, "start", map[string]any{"id": id}, nil); err != nil {
		// 已经 running 时 Engine 走 304 → 插件把它翻成成功 + already_in_state=true 也可，
		// 或直接返回 DOCKER_NOT_MODIFIED；两种都视作"符合语义"。
		if !isDockerNotModified(err) {
			t.Fatalf("start (already running) unexpected error: %v", err)
		}
	}

	// stop → restart：都应成功。
	if _, err := r.runDockerCommand(ctx, t, "stop",
		map[string]any{"id": id, "timeout_sec": 2}, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := r.runDockerCommand(ctx, t, "restart",
		map[string]any{"id": id, "timeout_sec": 2}, nil); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

// -----------------------------------------------------------------------------
// docker.logs（流式）
// -----------------------------------------------------------------------------

func TestDockerE2E_Logs(t *testing.T) {
	ok, hostOrReason := dockerE2EEnabled()
	if !ok {
		t.Skipf("docker e2e skipped: %s", hostOrReason)
	}
	r := newDockerRig(t, hostOrReason)

	// 用一个"输出后即退出"的容器，避免 follow=true 阻塞：
	// alpine 跑 echo hello-mow-e2e
	name := uniqueContainerName("logs")
	ensureImage(t, r, dockerE2EImage)
	id := createOneshot(t, r, name, dockerE2EImage,
		[]string{"echo", "hello-mow-e2e"})
	t.Cleanup(func() { forceRemove(t, r, id) })

	// 等待容器退出，logs 才能完整获取。
	waitContainerStopped(t, r, id, 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream := newMemStream(ctx, map[string]any{
		"id":     id,
		"follow": false,
		"tail":   "all",
	})
	err := r.Engine.RunStream(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "logs",
		Params:    stream.RawParams(),
		TargetID:  r.TargetID,
		Caller:    sdk.Caller{Type: sdk.CallerCLI, User: "e2e"},
	}, stream)
	if err != nil {
		t.Fatalf("docker.logs stream: %v", err)
	}
	out := stream.stdoutString()
	if !strings.Contains(out, "hello-mow-e2e") {
		t.Fatalf("logs stdout missing marker: %q", out)
	}
}

// -----------------------------------------------------------------------------
// docker.pull（流式 progress）
// -----------------------------------------------------------------------------

func TestDockerE2E_Pull(t *testing.T) {
	ok, hostOrReason := dockerE2EEnabled()
	if !ok {
		t.Skipf("docker e2e skipped: %s", hostOrReason)
	}
	r := newDockerRig(t, hostOrReason)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream := newMemStream(ctx, map[string]any{
		"from_image": "alpine",
		"tag":        "3.19",
	})
	err := r.Engine.RunStream(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "pull",
		Params:    stream.RawParams(),
		TargetID:  r.TargetID,
		Caller:    sdk.Caller{Type: sdk.CallerCLI, User: "e2e"},
	}, stream)
	if err != nil {
		t.Fatalf("docker.pull stream: %v", err)
	}
	// 至少应有 1 条 progress event（不同 daemon / cache 状态下条数不定，
	// 已缓存时也会 emit "Status: Image is up to date" 一行）。
	if stream.eventCount() == 0 {
		t.Fatalf("docker.pull produced no progress events")
	}
}

// -----------------------------------------------------------------------------
// docker.exec（非 TTY / 无 stdin）
// -----------------------------------------------------------------------------

func TestDockerE2E_Exec(t *testing.T) {
	ok, hostOrReason := dockerE2EEnabled()
	if !ok {
		t.Skipf("docker e2e skipped: %s", hostOrReason)
	}
	r := newDockerRig(t, hostOrReason)

	name := uniqueContainerName("exec")
	ensureImage(t, r, dockerE2EImage)
	id := createLongRunning(t, r, name, dockerE2EImage)
	t.Cleanup(func() { forceRemove(t, r, id) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream := newMemStream(ctx, map[string]any{
		"id":           id,
		"cmd":          []string{"sh", "-lc", "echo exec-ok && exit 0"},
		"tty":          false,
		"attach_stdin": false,
	})
	err := r.Engine.RunStream(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "exec",
		Params:    stream.RawParams(),
		TargetID:  r.TargetID,
		Caller:    sdk.Caller{Type: sdk.CallerCLI, User: "e2e"},
	}, stream)
	if err != nil {
		t.Fatalf("docker.exec stream: %v", err)
	}
	out := stream.stdoutString()
	if !strings.Contains(out, "exec-ok") {
		t.Fatalf("exec stdout missing marker: %q", out)
	}
	// exit_code 由 Finish(execResult, code) 携带；memStream 已捕获。
	if stream.finalExit != 0 {
		t.Fatalf("exec exit_code=%d, want 0", stream.finalExit)
	}
}

// -----------------------------------------------------------------------------
// docker.rm（Dangerous）
// -----------------------------------------------------------------------------

func TestDockerE2E_RmRequiresConfirmation(t *testing.T) {
	ok, hostOrReason := dockerE2EEnabled()
	if !ok {
		t.Skipf("docker e2e skipped: %s", hostOrReason)
	}
	r := newDockerRig(t, hostOrReason)

	name := uniqueContainerName("rm")
	ensureImage(t, r, dockerE2EImage)
	id := createLongRunning(t, r, name, dockerE2EImage)
	// 保底清理：某个断言失败时也要把容器删除，避免污染下一次运行。
	t.Cleanup(func() { forceRemove(t, r, id) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1) 不带 Confirmed：应被插件层 dangerous.go 直接拒。
	_, err := r.runDockerCommand(ctx, t, "rm",
		map[string]any{"id": id, "force": true}, nil)
	if err == nil {
		t.Fatalf("rm without confirmation should fail")
	}
	if !strings.Contains(err.Error(), "CONFIRMATION_REQUIRED") &&
		!strings.Contains(strings.ToLower(err.Error()), "confirmation") {
		t.Fatalf("rm without confirmation returned unexpected error: %v", err)
	}

	// 2) 带 Confirmed=true + force=true：允许移除运行中容器。
	if _, err := r.runDockerCommand(ctx, t, "rm",
		map[string]any{"id": id, "force": true}, nil,
		withConfirmed()); err != nil {
		t.Fatalf("rm with confirmation: %v", err)
	}
}

// -----------------------------------------------------------------------------
// helpers：容器操作（通过插件自身命令完成，避免 shell out 依赖）
// -----------------------------------------------------------------------------

// ensureImage 幂等地 pull 一次镜像；已存在时 Engine 会返回 "up to date"，仍算成功。
func ensureImage(t *testing.T, r *dockerRig, ref string) {
	t.Helper()
	image, tag := splitImageRef(ref)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stream := newMemStream(ctx, map[string]any{
		"from_image": image,
		"tag":        tag,
	})
	if err := r.Engine.RunStream(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "pull",
		Params:    stream.RawParams(),
		TargetID:  r.TargetID,
		Caller:    sdk.Caller{Type: sdk.CallerCLI, User: "e2e"},
	}, stream); err != nil {
		t.Fatalf("ensure image %s: %v", ref, err)
	}
}

func splitImageRef(ref string) (string, string) {
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, "latest"
}

// createLongRunning 借用 docker.exec 无法建 container 的限制，直接走 raw Engine API
// 创建 + 启动一个"睡到被杀"的容器。
//
// 我们不走 plugin 的 docker.create（v0.3 未提供），而是复用测试内自己的 HTTP 客户端。
// 这条路径**只在 E2E** 里用；生产代码禁止绕过 Command Engine。
func createLongRunning(t *testing.T, r *dockerRig, name, image string) string {
	t.Helper()
	return createContainerAndStart(t, r, name, image,
		[]string{"sh", "-lc", "sleep 3600"})
}

// createOneshot 创建一个"跑完 cmd 就退出"的容器；用于 docker.logs 场景。
func createOneshot(t *testing.T, r *dockerRig, name, image string, cmd []string) string {
	t.Helper()
	return createContainerAndStart(t, r, name, image, cmd)
}

// createContainerAndStart 用测试内 helper 直接调 Engine：
//   - POST /containers/create?name=xxx
//   - POST /containers/{id}/start
//
// 之所以不复用 plugin：docker.create 在 v0.3 里没做（roadmap v0.4）。
// helper 只是给 E2E 铺路，不进入 production 依赖。
func createContainerAndStart(t *testing.T, r *dockerRig, name, image string, cmd []string) string {
	t.Helper()
	c := newRawEngine(t, r.Host)
	body := map[string]any{
		"Image":     image,
		"Cmd":       cmd,
		"Tty":       false,
		"OpenStdin": false,
		"Labels":    map[string]string{"mow-e2e": "1"},
	}
	var created struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings,omitempty"`
	}
	if err := c.postJSON("/containers/create?name="+name, body, &created); err != nil {
		t.Fatalf("create container: %v", err)
	}
	if err := c.postJSON("/containers/"+created.ID+"/start", nil, nil); err != nil {
		t.Fatalf("start container: %v", err)
	}
	return created.ID
}

// forceRemove 幂等清理；错误只记 log，不 Fatal（cleanup 阶段常出现 already gone）。
func forceRemove(t *testing.T, r *dockerRig, id string) {
	t.Helper()
	c := newRawEngine(t, r.Host)
	if err := c.deleteNoBody("/containers/" + id + "?force=true&v=true"); err != nil {
		t.Logf("cleanup rm %s: %v (ignored)", id, err)
	}
}

// waitContainerStopped 轮询 /inspect 直到 State.Running=false。
func waitContainerStopped(t *testing.T, r *dockerRig, id string, timeout time.Duration) {
	t.Helper()
	c := newRawEngine(t, r.Host)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var got struct {
			State struct {
				Running bool `json:"Running"`
			} `json:"State"`
		}
		if err := c.getJSON("/containers/"+id+"/json", &got); err == nil && !got.State.Running {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("container %s did not stop within %s", id, timeout)
}

// -----------------------------------------------------------------------------
// helpers：容器名 / 断言 / 错误分类
// -----------------------------------------------------------------------------

// uniqueContainerName 用测试专属前缀 + 纳秒时间戳，避免同一 host 上并行跑 CI 的碰撞。
func uniqueContainerName(kind string) string {
	return "mow-e2e-" + kind + "-" + timestampSuffix()
}

func timestampSuffix() string {
	return strings.ReplaceAll(time.Now().Format("20060102T150405.000000000"), ".", "")
}

func containsContainer(list []dockerContainerBrief, id, name string) bool {
	for _, c := range list {
		if strings.HasPrefix(c.ID, id[:12]) || strings.HasPrefix(id, c.ID) {
			return true
		}
		for _, n := range c.Names {
			// Engine 返回的 name 带前导 "/"，我们要用 TrimPrefix 比对。
			if strings.TrimPrefix(n, "/") == name {
				return true
			}
		}
	}
	return false
}

// isDockerNotModified 判断错误是否属于"目标已处于期望状态"。
// plugin 端把 304 翻成 sdk.Error{Code: DOCKER_NOT_MODIFIED}；
// 或 lifecycle 直接把它包装为 already_in_state=true 的 200 —— 两条路径都算通过。
func isDockerNotModified(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "DOCKER_NOT_MODIFIED") ||
		strings.Contains(strings.ToLower(err.Error()), "not modified") ||
		strings.Contains(strings.ToLower(err.Error()), "already")
}

// -----------------------------------------------------------------------------
// memStream —— sdk.Stream 的内存实现，供测试消费流式 Command 的输出
// -----------------------------------------------------------------------------

type memStream struct {
	ctx      context.Context
	params   json.RawMessage
	auditID  string
	incoming chan sdk.Incoming

	mu        sync.Mutex
	stdout    []byte
	stderr    []byte
	events    []json.RawMessage
	finalData any
	finalExit int
	finished  bool
}

func newMemStream(ctx context.Context, params any) *memStream {
	raw, _ := json.Marshal(params)
	return &memStream{
		ctx:      ctx,
		params:   raw,
		incoming: make(chan sdk.Incoming),
	}
}

// SetAuditID 实现 command.AuditIDSetter。
func (s *memStream) SetAuditID(id string) { s.auditID = id }

func (s *memStream) Context() context.Context     { return s.ctx }
func (s *memStream) AuditID() string              { return s.auditID }
func (s *memStream) Caller() sdk.Caller           { return sdk.Caller{Type: sdk.CallerCLI, User: "e2e"} }
func (s *memStream) Confirmed() bool              { return true }
func (s *memStream) RawParams() json.RawMessage   { return s.params }
func (s *memStream) Params(dst any) error         { return json.Unmarshal(s.params, dst) }
func (s *memStream) Connection() *sdk.Connection  { return nil } // 由 ResolveConnectionMiddleware 注入
func (s *memStream) Recv() <-chan sdk.Incoming    { return s.incoming }

func (s *memStream) Stdout(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stdout = append(s.stdout, data...)
	return nil
}
func (s *memStream) Stderr(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stderr = append(s.stderr, data...)
	return nil
}
func (s *memStream) Event(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, raw)
	return nil
}
func (s *memStream) Finish(finalData any, exitCode int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalData = finalData
	s.finalExit = exitCode
	s.finished = true
	return nil
}

func (s *memStream) stdoutString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.stdout)
}
func (s *memStream) eventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}
