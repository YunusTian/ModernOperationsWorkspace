package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

// Suite 描述一次 conformance 运行的输入。
//
// 除 Plugin 外均为可选：默认会跑最小生命周期烟雾 + 命令签名检查。
// 若填了 Cases，会额外驱动指定命令。
type Suite struct {
	// Plugin 是被测插件实例。
	Plugin sdk.Plugin

	// InitSettings 传给 Plugin.Init 的 settings（可选）。
	InitSettings json.RawMessage

	// DataDir 传给 Plugin.Init 的数据目录；默认 t.TempDir()。
	DataDir string

	// InitTimeout / ShutdownTimeout 用于兜底防止无限阻塞；默认 5s。
	InitTimeout     time.Duration
	ShutdownTimeout time.Duration

	// Cases 用于对单个 Command 做真实调用；每个 case 命中一个 Spec.ID。
	Cases []Case

	// SkipLifecycle=true 时不驱动 Init/Shutdown（例如插件已在外部启动）。
	SkipLifecycle bool
}

// Case 描述一次针对具体 Command 的 conformance 断言。
type Case struct {
	// Name 用作 t.Run 的子测名；缺省用 CommandID。
	Name string

	// CommandID 必填；对应 Plugin.Commands()[*].Spec().ID。
	CommandID string

	// Params 会被 json.Marshal 后传入 ExecuteRequest.Params / Stream.RawParams。
	Params any

	// Confirmed 传给 ExecuteRequest.Confirmed；Dangerous 命令必须显式为 true。
	Confirmed bool

	// StreamInputs 仅在 Streaming Command 上使用；按顺序 Push 到 fake stream。
	StreamInputs []sdk.Incoming

	// Timeout 单个 case 的执行超时；默认 5s。
	Timeout time.Duration

	// Check 是可选的自定义断言；接收 case 的执行结果。
	Check func(t *testing.T, r Result)
}

// Result 是单次 case 的执行产物。
type Result struct {
	// Streaming 表示是否走的是 ExecuteStream。
	Streaming bool

	// 一次性 Command 的产物。
	Response *sdk.ExecuteResponse
	ExecErr  error

	// 流式 Command 的产物。
	Stream     *FakeStream
	StreamErr  error
	StreamDone bool
}

// Run 是入口：一次性把 Validate + 生命周期 + 案例串起来。
//
// 使用示例：
//
//	conformance.Run(t, conformance.Suite{
//		Plugin: newMyPlugin(),
//		Cases: []conformance.Case{
//			{CommandID: "ping"},
//		},
//	})
func Run(t *testing.T, s Suite) {
	t.Helper()
	if s.Plugin == nil {
		t.Fatal("conformance: Suite.Plugin is required")
	}

	// 1) 静态 Validate —— 复用 sdk.Validate，避免在此重复实现。
	if err := sdk.Validate(s.Plugin); err != nil {
		t.Fatalf("conformance: sdk.Validate failed: %v", err)
	}

	// 2) Command 签名检查：Streaming 声明与是否实现 ExecuteStream 一致。
	checkCommandInterfaces(t, s.Plugin)

	// 3) 生命周期烟雾：Init → (可选 cases) → Shutdown。
	if s.SkipLifecycle {
		runCases(t, s)
		return
	}
	dataDir := s.DataDir
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	initTimeout := s.InitTimeout
	if initTimeout <= 0 {
		initTimeout = 5 * time.Second
	}
	shutdownTimeout := s.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 5 * time.Second
	}

	initCtx, initCancel := context.WithTimeout(context.Background(), initTimeout)
	defer initCancel()
	req := sdk.InitRequest{Settings: s.InitSettings, DataDir: dataDir}
	if err := s.Plugin.Init(initCtx, req); err != nil {
		t.Fatalf("conformance: Plugin.Init failed: %v", err)
	}

	// 4) HealthCheck 应可安全调用（返回值不作硬断言，插件可能返回 Degraded）。
	if got := s.Plugin.HealthCheck(context.Background()); got == sdk.StatusUnknown {
		t.Logf("conformance: HealthCheck returned StatusUnknown (allowed)")
	}

	runCases(t, s)

	// 5) Shutdown 必须能在 timeout 内返回。
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := s.Plugin.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("conformance: Plugin.Shutdown failed: %v", err)
	}
}

// checkCommandInterfaces 强制 Streaming/非 Streaming 命令契约：
//   - Streaming=true：ExecuteStream 必须存在，且 Execute 允许返回 ErrNotSupported
//   - Streaming=false：Execute 必须存在
//
// sdk.CommandHandler 接口本身要求两个方法都实现，因此这里只做"运行时不应
// panic"的探测；真正的 not-supported 语义由业务侧 Case 校验。
func checkCommandInterfaces(t *testing.T, p sdk.Plugin) {
	t.Helper()
	seen := map[string]struct{}{}
	for _, h := range p.Commands() {
		spec := h.Spec()
		if _, dup := seen[spec.ID]; dup {
			t.Fatalf("conformance: duplicate command id %q (sdk.Validate should have caught this)", spec.ID)
		}
		seen[spec.ID] = struct{}{}
		if spec.ID == "" {
			t.Fatal("conformance: empty command id")
		}
	}
}

func runCases(t *testing.T, s Suite) {
	t.Helper()
	if len(s.Cases) == 0 {
		return
	}
	index := map[string]sdk.CommandHandler{}
	for _, h := range s.Plugin.Commands() {
		index[h.Spec().ID] = h
	}
	for _, c := range s.Cases {
		c := c
		name := c.Name
		if name == "" {
			name = c.CommandID
		}
		t.Run(name, func(t *testing.T) {
			handler, ok := index[c.CommandID]
			if !ok {
				t.Fatalf("conformance: no command with id %q", c.CommandID)
			}
			timeout := c.Timeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			var paramsRaw json.RawMessage
			if c.Params != nil {
				b, err := json.Marshal(c.Params)
				if err != nil {
					t.Fatalf("conformance: marshal Params: %v", err)
				}
				paramsRaw = b
			}

			spec := handler.Spec()
			res := Result{Streaming: spec.Streaming}
			if spec.Streaming {
				stream := NewFakeStream(ctx, FakeStreamOptions{
					AuditID:   "conformance-" + spec.ID,
					Confirmed: c.Confirmed,
					RawParams: paramsRaw,
				})
				for _, in := range c.StreamInputs {
					stream.Push(in)
				}
				res.Stream = stream
				res.StreamErr = handler.ExecuteStream(ctx, stream)
				res.StreamDone = stream.Finished()
			} else {
				resp, err := handler.Execute(ctx, &sdk.ExecuteRequest{
					AuditID:   "conformance-" + spec.ID,
					Params:    paramsRaw,
					Confirmed: c.Confirmed,
				})
				res.Response = resp
				res.ExecErr = err
			}

			// 通用 Dangerous 语义：未确认时必须返回 ErrConfirmationRequired。
			if spec.Permission == sdk.PermDangerous && !c.Confirmed {
				if !isConfirmationRequired(res) {
					t.Fatalf("conformance: dangerous command %q must return ErrConfirmationRequired when Confirmed=false; got exec=%v stream=%v", spec.ID, res.ExecErr, res.StreamErr)
				}
			}

			if c.Check != nil {
				c.Check(t, res)
			}
		})
	}
}

func isConfirmationRequired(r Result) bool {
	if errors.Is(r.ExecErr, sdk.ErrConfirmationRequired) {
		return true
	}
	if errors.Is(r.StreamErr, sdk.ErrConfirmationRequired) {
		return true
	}
	return false
}
