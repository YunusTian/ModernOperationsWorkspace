package e2e

// ai_e2e_test.go —— AI Plugin + 宿主 orchestrator 端到端测试。
//
// 测试范围（对齐 docs/v0.4-acceptance-checklist.md §6 的 "Windows + Linux mock/fake-provider E2E"）：
//  1. mock provider → orchestrator.Run 走一整轮，审计事件序列完整
//  2. OpenAI-compatible provider（fake httptest.Server）
//     a. 一次性 chat 主路径 + usage
//     b. 429 → 200 有上限退避重试
//     c. tool_call → 宿主执行 ai 内白名单命令（ai.list_providers 是 Read）后回填 tool 消息，收敛为 stop
//  3. 主流程结束后 SlogAuditor 至少写出一条 loop.end
//
// 与 core/ai 单测的差别：这里跑真实 gRPC 插件子进程 + 真实 HTTP 传输，
// 覆盖 Provider ↔ 宿主之间的序列化 / 错误码传播 / 重试 sleep / 事件闭环。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreai "github.com/mow/mow/core/ai"
	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginclient"
)

// -----------------------------------------------------------------------------
// 构建 & 加载 AI Plugin
// -----------------------------------------------------------------------------

// aiPluginBinary 保存本次测试运行编译出的 ai plugin 二进制路径。
// 通过 buildAIPluginOnce 惰性编译，避免每个子测试都跑一次 `go build`。
var aiPluginBinary string
var aiPluginBuilt atomic.Bool

// buildAIPluginOnce 编译 plugins/ai 到 t.TempDir()。CI 可通过环境变量
// MOW_AI_PLUGIN 复用外部预编译产物。
func buildAIPluginOnce(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("MOW_AI_PLUGIN"); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("MOW_AI_PLUGIN=%s not accessible: %v", bin, err)
		}
		return bin
	}
	if aiPluginBuilt.Load() && aiPluginBinary != "" {
		return aiPluginBinary
	}
	dir, err := os.MkdirTemp("", "mow-ai-e2e-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bin := filepath.Join(dir, "ai-plugin"+execSuffix())
	src, err := findModuleDir("../../plugins/ai")
	if err != nil {
		t.Fatalf("find plugins/ai: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build ai plugin: %v", err)
	}
	aiPluginBinary = bin
	aiPluginBuilt.Store(true)
	return bin
}

// aiRig 是 AI E2E 的最小执行环境：一次 t 内自动清理插件与临时目录。
type aiRig struct {
	Engine  *command.Engine
	PlugMgr *plugin.Manager
	Loaded  *pluginclient.LoadedPlugin
	DataDir string
}

// newAIRig 装配 AI plugin + Command Engine，settings 由调用方传入。
func newAIRig(t *testing.T, settings map[string]any) *aiRig {
	t.Helper()
	bin := buildAIPluginOnce(t)
	dataDir := t.TempDir()
	log := logger.Default()
	plugMgr := plugin.NewManager(plugin.Options{Logger: log, DataDir: dataDir})
	engine := command.New(command.Options{Manager: plugMgr, Logger: log})

	lp, err := pluginclient.LoadFromBinary(bin, nil)
	if err != nil {
		t.Fatalf("load ai plugin: %v", err)
	}
	if err := plugMgr.Register(lp.Plugin); err != nil {
		lp.Close()
		t.Fatalf("register: %v", err)
	}
	settingsJSON, _ := json.Marshal(settings)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := plugMgr.Enable(ctx, "ai", sdk.InitRequest{DataDir: dataDir, Settings: settingsJSON}); err != nil {
		lp.Close()
		t.Fatalf("enable ai: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = plugMgr.Shutdown(shutdownCtx)
		lp.Close()
	})
	return &aiRig{Engine: engine, PlugMgr: plugMgr, Loaded: lp, DataDir: dataDir}
}

// -----------------------------------------------------------------------------
// Fake OpenAI-compatible HTTP server
// -----------------------------------------------------------------------------

// fakeOpenAI 是一个可编程的假 OpenAI 服务器：给每一次 chat/completions 请求
// 挂一个 responder；用于覆盖 429 重试、tool_call 循环、正常 stop 主路径。
type fakeOpenAI struct {
	Srv       *httptest.Server
	Requests  atomic.Int32
	responder func(w http.ResponseWriter, r *http.Request, seq int32)
}

func newFakeOpenAI(t *testing.T, responder func(w http.ResponseWriter, r *http.Request, seq int32)) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{responder: responder}
	f.Srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seq := f.Requests.Add(1)
		f.responder(w, r, seq)
	}))
	t.Cleanup(f.Srv.Close)
	return f
}

// writeChatJSON 是一个响应助手：写一条标准 OpenAI ChatCompletion 响应。
func writeChatJSON(w http.ResponseWriter, msg map[string]any, usage map[string]int, finish string) {
	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{
		"choices": []map[string]any{{"message": msg, "finish_reason": finish}},
		"usage":   usage,
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// -----------------------------------------------------------------------------
// Auditor：把事件收集到内存，供断言使用
// -----------------------------------------------------------------------------

type e2eAuditor struct {
	mu     atomic.Int32 // 事件计数
	events []coreai.Event
	ch     chan coreai.Event
}

func newE2EAuditor(cap int) *e2eAuditor { return &e2eAuditor{ch: make(chan coreai.Event, cap)} }

func (a *e2eAuditor) OnEvent(_ context.Context, ev coreai.Event) {
	a.mu.Add(1)
	a.events = append(a.events, ev)
	// 非阻塞写入，避免测试挂起。
	select {
	case a.ch <- ev:
	default:
	}
}

func (a *e2eAuditor) types() []coreai.EventType {
	out := make([]coreai.EventType, 0, len(a.events))
	for _, e := range a.events {
		out = append(out, e.Type)
	}
	return out
}

// -----------------------------------------------------------------------------
// 1. mock provider 端到端
// -----------------------------------------------------------------------------

func TestAIE2E_MockProviderThroughOrchestrator(t *testing.T) {
	rig := newAIRig(t, map[string]any{
		"providers": []map[string]any{{"name": "mock", "kind": "mock"}},
	})

	audit := newE2EAuditor(16)
	orch, err := coreai.New(coreai.Options{
		Runner:   engineAsRunner{rig.Engine},
		Auditor:  audit,
		Redactor: command.RedactParams,
	})
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	res, err := orch.Run(context.Background(), coreai.Request{
		Provider:  "mock",
		Messages:  []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hello"}},
		SessionID: "e2e-mock",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(res.Response.Message.Content, "[mock]") {
		t.Fatalf("mock content: %q", res.Response.Message.Content)
	}
	// 事件序列：LoopStart → RoundStart → RoundEnd → LoopEnd
	want := []coreai.EventType{
		coreai.EventLoopStart, coreai.EventRoundStart, coreai.EventRoundEnd, coreai.EventLoopEnd,
	}
	got := audit.types()
	if len(got) != len(want) {
		t.Fatalf("event count: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %s want %s (all=%v)", i, got[i], want[i], got)
		}
	}
	last := audit.events[len(audit.events)-1]
	if last.FinishReason != sdk.FinishStop {
		t.Fatalf("loop_end finish=%s", last.FinishReason)
	}
}

// -----------------------------------------------------------------------------
// 2a. OpenAI-compatible: 一次性 chat 主路径 + usage
// -----------------------------------------------------------------------------

func TestAIE2E_OpenAIProviderChatHappyPath(t *testing.T) {
	fake := newFakeOpenAI(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-e2e" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		writeChatJSON(w,
			map[string]any{"role": "assistant", "content": "e2e-ok"},
			map[string]int{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8},
			"stop",
		)
	})
	t.Setenv("MOW_E2E_OPENAI_KEY", "sk-e2e")

	rig := newAIRig(t, map[string]any{
		"providers": []map[string]any{{
			"name": "openai", "kind": "openai",
			"options": map[string]any{
				"base_url":      fake.Srv.URL,
				"api_key_env":   "MOW_E2E_OPENAI_KEY",
				"default_model": "gpt-e2e",
			},
		}},
	})
	audit := newE2EAuditor(16)
	orch, err := coreai.New(coreai.Options{
		Runner: engineAsRunner{rig.Engine}, Auditor: audit, Redactor: command.RedactParams,
	})
	if err != nil {
		t.Fatalf("orch: %v", err)
	}
	res, err := orch.Run(context.Background(), coreai.Request{
		Provider: "openai", Model: "gpt-e2e",
		Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Response.Message.Content != "e2e-ok" {
		t.Fatalf("content=%q", res.Response.Message.Content)
	}
	if res.Response.Usage.TotalTokens != 8 {
		t.Fatalf("usage=%+v", res.Response.Usage)
	}
	if fake.Requests.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", fake.Requests.Load())
	}
	if want, last := sdk.FinishStop, audit.events[len(audit.events)-1]; last.FinishReason != want {
		t.Fatalf("loop_end finish=%s want %s", last.FinishReason, want)
	}
}

// -----------------------------------------------------------------------------
// 2b. OpenAI-compatible: 429 → 200 有上限退避重试
// -----------------------------------------------------------------------------

func TestAIE2E_OpenAIProviderRetryOn429(t *testing.T) {
	fake := newFakeOpenAI(t, func(w http.ResponseWriter, r *http.Request, seq int32) {
		if seq < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		writeChatJSON(w,
			map[string]any{"role": "assistant", "content": "recovered"},
			map[string]int{"total_tokens": 4}, "stop",
		)
	})
	t.Setenv("MOW_E2E_OPENAI_KEY", "sk-e2e")

	rig := newAIRig(t, map[string]any{
		"providers": []map[string]any{{
			"name": "openai", "kind": "openai",
			"options": map[string]any{
				"base_url":               fake.Srv.URL,
				"api_key_env":            "MOW_E2E_OPENAI_KEY",
				"default_model":          "gpt-e2e",
				"retry_max_attempts":     3,
				"retry_base_backoff_ms":  1, // 1ms → 端到端测试不真的等
				"retry_max_backoff_ms":   5,
			},
		}},
	})
	orch, err := coreai.New(coreai.Options{Runner: engineAsRunner{rig.Engine}, Redactor: command.RedactParams})
	if err != nil {
		t.Fatalf("orch: %v", err)
	}
	res, err := orch.Run(context.Background(), coreai.Request{
		Provider: "openai", Model: "gpt-e2e",
		Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "retry"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Response.Message.Content != "recovered" {
		t.Fatalf("content=%q", res.Response.Message.Content)
	}
	// 期望：第 1、2 次 429，第 3 次 200 —— 共 3 次上游请求。
	if got := fake.Requests.Load(); got != 3 {
		t.Fatalf("upstream calls=%d, want 3 (2×429 + 1×200)", got)
	}
}

// -----------------------------------------------------------------------------
// 2c. OpenAI-compatible: tool_call 走宿主白名单 command，收敛为 stop
// -----------------------------------------------------------------------------

func TestAIE2E_OpenAIProviderToolCallToWhitelistedCommand(t *testing.T) {
	fake := newFakeOpenAI(t, func(w http.ResponseWriter, r *http.Request, seq int32) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		if seq == 1 {
			// 首轮：模型请求调用 probe.info（Read 权限，在 allowlist 内）。
			// 之所以不用 ai.list_providers：orchestrator 显式禁止 ai.* 递归，
			// 因此这里用一个专门为 e2e 注册的 in-process probe 插件。
			writeChatJSON(w,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":   "call-1",
						"type": "function",
						"function": map[string]any{
							"name":      "probe.info",
							"arguments": `{"key":"platform"}`,
						},
					}},
				},
				map[string]int{"total_tokens": 6}, "tool_calls",
			)
			return
		}
		// 第二轮：宿主已把工具结果作为 role=tool 追加，模型给出最终回答
		if !bytes.Contains(body, []byte(`"role":"tool"`)) {
			http.Error(w, "expected tool result in messages", http.StatusBadRequest)
			return
		}
		writeChatJSON(w,
			map[string]any{"role": "assistant", "content": "probe done"},
			map[string]int{"total_tokens": 10}, "stop",
		)
	})
	t.Setenv("MOW_E2E_OPENAI_KEY", "sk-e2e")

	rig := newAIRig(t, map[string]any{
		"providers": []map[string]any{{
			"name": "openai", "kind": "openai",
			"options": map[string]any{
				"base_url": fake.Srv.URL, "api_key_env": "MOW_E2E_OPENAI_KEY", "default_model": "gpt-e2e",
			},
		}},
	})
	// 追加注册 in-process probe 插件（提供 probe.info Read Command）。
	probe := &probePlugin{}
	if err := rig.PlugMgr.Register(probe); err != nil {
		t.Fatalf("register probe: %v", err)
	}
	if err := rig.PlugMgr.Enable(context.Background(), "probe", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable probe: %v", err)
	}

	audit := newE2EAuditor(32)
	orch, err := coreai.New(coreai.Options{
		Runner:       engineAsRunner{rig.Engine},
		AllowedTools: []string{"probe.info"},
		Auditor:      audit,
		Redactor:     command.RedactParams,
	})
	if err != nil {
		t.Fatalf("orch: %v", err)
	}
	res, err := orch.Run(context.Background(), coreai.Request{
		Provider: "openai", Model: "gpt-e2e",
		Messages: []sdk.ChatMessage{{Role: sdk.RoleUser, Content: "probe please"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Response.Message.Content != "probe done" {
		t.Fatalf("content=%q", res.Response.Message.Content)
	}
	if res.ToolCalls != 1 || res.Rounds != 2 {
		t.Fatalf("rounds=%d tool_calls=%d, want 2/1", res.Rounds, res.ToolCalls)
	}
	// 断言事件里恰好一条 ai.tool.call，且没有 Rejected
	var toolCalls []coreai.Event
	for _, e := range audit.events {
		if e.Type == coreai.EventToolCall {
			toolCalls = append(toolCalls, e)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool_call events=%d, want 1 (types=%v)", len(toolCalls), audit.types())
	}
	if toolCalls[0].Rejected {
		t.Fatalf("tool_call unexpectedly rejected: %+v", toolCalls[0])
	}
	if toolCalls[0].ToolName != "probe.info" {
		t.Fatalf("tool_call name=%s", toolCalls[0].ToolName)
	}
	if fake.Requests.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2", fake.Requests.Load())
	}
	// probe.info 被真实调用了一次
	if probe.calls.Load() != 1 {
		t.Fatalf("probe.info invocations=%d, want 1", probe.calls.Load())
	}
}

// -----------------------------------------------------------------------------
// probePlugin：为 tool-call e2e 提供一个 in-process Read command。
// -----------------------------------------------------------------------------

// probePlugin 是一个极简 in-process 插件，仅供 AI E2E tool-call 用例使用。
// 提供一条 probe.info Read Command，接受 {"key": string} 参数，返回 {"value": string}。
type probePlugin struct{ calls atomic.Int32 }

func (p *probePlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "probe", Name: "Probe", Version: "0.0.1", CoreVersion: ">=0.1.0"}
}
func (p *probePlugin) Init(context.Context, sdk.InitRequest) error   { return nil }
func (p *probePlugin) Shutdown(context.Context) error                { return nil }
func (p *probePlugin) HealthCheck(context.Context) sdk.HealthStatus  { return sdk.StatusHealthy }
func (p *probePlugin) Commands() []sdk.CommandHandler {
	return []sdk.CommandHandler{&probeInfoCmd{owner: p}}
}

type probeInfoCmd struct{ owner *probePlugin }

func (c *probeInfoCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "info",
		Description: "Return a canned probe info value.",
		Permission:  sdk.PermRead,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
	}
}
func (c *probeInfoCmd) Execute(_ context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	c.owner.calls.Add(1)
	var in struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(req.Params, &in); err != nil {
		return nil, err
	}
	out, _ := json.Marshal(map[string]string{"value": "probe:" + in.Key})
	return &sdk.ExecuteResponse{Data: out}, nil
}
func (c *probeInfoCmd) ExecuteStream(context.Context, sdk.Stream) error { return nil }

// -----------------------------------------------------------------------------
// Adapters
// -----------------------------------------------------------------------------

// engineAsRunner 把 *command.Engine 适配到 coreai.CommandRunner。
type engineAsRunner struct{ engine *command.Engine }

func (e engineAsRunner) Run(ctx context.Context, req command.Request) (*command.Response, error) {
	return e.engine.Run(ctx, req)
}
func (e engineAsRunner) Spec(pluginID, commandID string) (sdk.CommandSpec, error) {
	return e.engine.Spec(pluginID, commandID)
}

// helper: 让 orchestrator 构造错误更可读，测试断言时用。
var _ = fmt.Sprintf
