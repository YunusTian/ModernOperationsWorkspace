// Package main —— mow ssh <target>：CLI 快捷进入远端交互式 shell。
//
// 后端能力已由 SSH 插件的 ssh.shell（Streaming Command）实现；
// CLI 侧只需要：
//   1) 实现一个 sdk.Stream，把本地 os.Stdin/Stdout/Stderr 与远端 PTY 桥接
//   2) 让终端进入 raw 模式并订阅 SIGWINCH，把大小变化推给远端
//
// 使用：
//   mow ssh srv01                                # 直接进入 shell
//   mow ssh srv01 --term xterm --rows 40 --cols 120
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// mow ssh <target>
// -----------------------------------------------------------------------------

type sshCmdOpts struct {
	Term string
	Rows int
	Cols int
}

func newSSHCmd(h *appHolder) *cobra.Command {
	o := &sshCmdOpts{}
	cmd := &cobra.Command{
		Use:   "ssh <target>",
		Short: "Open an interactive SSH shell on the given target",
		Long: `Open an interactive PTY shell on the given target via the SSH plugin.

The Command Engine routes to ssh.shell (streaming). Local stdin is put into
raw mode; window resize (SIGWINCH) is forwarded to the remote PTY.

Example:
  mow ssh srv01
  mow ssh srv01 --term xterm-256color --rows 40 --cols 120`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runSSHShell(h, args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Term, "term", "", "TERM value (default: $TERM or xterm-256color)")
	f.IntVar(&o.Rows, "rows", 0, "override PTY rows (default: current tty)")
	f.IntVar(&o.Cols, "cols", 0, "override PTY cols (default: current tty)")
	return cmd
}

func runSSHShell(h *appHolder, targetID string, o *sshCmdOpts) error {
	app, err := h.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer app.Close(ctx)

	if err := app.ensurePluginEnabled(ctx, "ssh"); err != nil {
		return err
	}

	// stdin/stdout 必须是 TTY，否则 raw / GetSize 都没意义
	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return fmt.Errorf("mow ssh: stdin is not a terminal")
	}

	// 初始 rows/cols
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	if o.Rows > 0 {
		rows = o.Rows
	}
	if o.Cols > 0 {
		cols = o.Cols
	}
	termName := o.Term
	if termName == "" {
		if v := os.Getenv("TERM"); v != "" {
			termName = v
		} else {
			termName = "xterm-256color"
		}
	}

	// 提前 open connection —— 与 desktop 同理：core/command.RunStream 不会把
	// Connection 注入 stream，我们需要手动挂上。
	conn, err := app.ConnMgr.Open(ctx, targetID)
	if err != nil {
		return fmt.Errorf("open target %q: %w", targetID, err)
	}

	params, _ := json.Marshal(map[string]any{
		"term": termName,
		"rows": rows,
		"cols": cols,
	})

	stream := newCLIShellStream(ctx, conn, params)

	// 进入 raw 模式；退出前恢复
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer func() { _ = term.Restore(stdinFd, oldState) }()

	// stdin → stream.pushStdin
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				stream.pushStdin(chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	// SIGWINCH → stream.pushWinch（Windows 上没有 SIGWINCH，做兜底轮询）
	stopResize := installWinchWatcher(func() {
		if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			stream.pushWinch(r, c)
		}
	})
	defer stopResize()

	req := command.Request{
		PluginID:   "ssh",
		CommandID:  "shell",
		TargetID:   targetID,
		Connection: conn,
		Params:     params,
		Caller:     sdk.Caller{Type: sdk.CallerCLI, User: currentUser()},
	}

	err = app.Engine.RunStream(ctx, req, stream)
	// 恢复 tty 之前把最后一行落地，避免 prompt 残影
	fmt.Fprint(os.Stdout, "\r\n")
	if err != nil {
		return err
	}
	if code := stream.exitCode(); code != 0 {
		return fmt.Errorf("remote exit code %d", code)
	}
	return nil
}

// -----------------------------------------------------------------------------
// cliShellStream —— sdk.Stream 的 CLI 侧实现
// -----------------------------------------------------------------------------

type cliShellStream struct {
	ctx      context.Context
	conn     *sdk.Connection
	params   json.RawMessage
	incoming chan sdk.Incoming
	finished atomic.Bool
	exitCd   atomic.Int32

	writeMu   sync.Mutex // 保证 stdout/stderr 顺序
	closeOnce sync.Once
}

func newCLIShellStream(ctx context.Context, conn *sdk.Connection, params json.RawMessage) *cliShellStream {
	return &cliShellStream{
		ctx:      ctx,
		conn:     conn,
		params:   params,
		incoming: make(chan sdk.Incoming, 32),
	}
}

func (s *cliShellStream) exitCode() int { return int(s.exitCd.Load()) }

// -----------------------------------------------------------------------------
// sdk.Stream 接口
// -----------------------------------------------------------------------------

func (s *cliShellStream) Context() context.Context   { return s.ctx }
func (s *cliShellStream) AuditID() string            { return "" }
func (s *cliShellStream) Caller() sdk.Caller         { return sdk.Caller{Type: sdk.CallerCLI} }
func (s *cliShellStream) Confirmed() bool            { return true }
func (s *cliShellStream) RawParams() json.RawMessage { return s.params }
func (s *cliShellStream) Params(dst any) error {
	if len(s.params) == 0 {
		return nil
	}
	return json.Unmarshal(s.params, dst)
}
func (s *cliShellStream) Connection() *sdk.Connection { return s.conn }
func (s *cliShellStream) Recv() <-chan sdk.Incoming   { return s.incoming }

func (s *cliShellStream) Stdout(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := os.Stdout.Write(data)
	return err
}

func (s *cliShellStream) Stderr(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := os.Stderr.Write(data)
	return err
}

func (s *cliShellStream) Event(v any) error {
	// CLI 侧不消费结构化 event；忽略。
	_ = v
	return nil
}

func (s *cliShellStream) Finish(finalData any, exitCode int) error {
	if s.finished.Swap(true) {
		return nil
	}
	s.exitCd.Store(int32(exitCode))
	// 若插件带回了 finalData（例如 {"exit_code": N}），尝试再兜底一次
	if finalData != nil {
		if b, err := json.Marshal(finalData); err == nil {
			var r struct {
				ExitCode *int `json:"exit_code"`
			}
			if json.Unmarshal(b, &r) == nil && r.ExitCode != nil && *r.ExitCode != exitCode {
				s.exitCd.Store(int32(*r.ExitCode))
			}
		}
	}
	return nil
}

func (s *cliShellStream) pushStdin(b []byte) {
	select {
	case s.incoming <- &sdk.Stdin{Data: b, At: time.Now()}:
	case <-s.ctx.Done():
	}
}

func (s *cliShellStream) pushWinch(rows, cols int) {
	payload, _ := json.Marshal(map[string]int{"rows": rows, "cols": cols})
	select {
	case s.incoming <- &sdk.Signal{Type: sdk.SignalWinch, Payload: payload, At: time.Now()}:
	case <-s.ctx.Done():
	}
}

func (s *cliShellStream) close() {
	s.closeOnce.Do(func() { close(s.incoming) })
}

// 保留 close 供未来调用（例如客户端主动关闭输入端）。
var _ = (*cliShellStream)(nil).close

// -----------------------------------------------------------------------------
// SIGWINCH 观察（跨平台）
// -----------------------------------------------------------------------------

// installWinchWatcher 在支持 SIGWINCH 的系统上订阅信号；
// Windows 上没有此信号，回退到定时轮询窗口大小。
// 返回一个停止函数。
func installWinchWatcher(onChange func()) func() {
	if !signalWinchSupported() {
		// Windows：500ms 轮询一次
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			lastC, lastR := -1, -1
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					c, r, err := term.GetSize(int(os.Stdout.Fd()))
					if err != nil {
						continue
					}
					if c != lastC || r != lastR {
						lastC, lastR = c, r
						onChange()
					}
				}
			}
		}()
		return func() { close(stop) }
	}

	// 类 Unix：真信号
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigWinch())
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ch:
				onChange()
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(stop)
	}
}

