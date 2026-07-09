package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// ssh.shell —— 交互式 PTY 会话（流式 Command）
// -----------------------------------------------------------------------------
//
// 语义：
//   - Client 侧通过 sdk.Stream 首帧下发 shellParams（term / rows / cols / env）
//   - 后续 Stdin 帧写入远端 PTY
//   - Signal:
//       - SignalWinch { rows, cols }  → session.WindowChange
//       - SignalInt                   → session.Signal(SIGINT)
//       - SignalTerm/Kill/Cancel      → 关闭 session（触发退出）
//   - 远端 stdout/stderr 通过 s.Stdout/s.Stderr 推回
//   - session.Wait() 返回时用 s.Finish 结束，exitCode 透传

// shellParams 是 ssh.shell 首帧参数。
type shellParams struct {
	Term string            `json:"term,omitempty"` // 默认 xterm-256color
	Rows int               `json:"rows,omitempty"` // 默认 24
	Cols int               `json:"cols,omitempty"` // 默认 80
	Env  map[string]string `json:"env,omitempty"`  // 尽力设置，服务端可能拒绝
}

// shellResult 是 ssh.shell 结束时随 Finish 返回的最终数据。
type shellResult struct {
	ExitCode int `json:"exit_code"`
}

// winchPayload 是 SignalWinch 的 Payload JSON 结构。
type winchPayload struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// shellCmd 是 ssh.shell 的实现。
// 权限：Execute（等价于用户在远端 tty 内敲键盘）。
type shellCmd struct {
	pool   *SessionPool
	plugin *SSHPlugin
}

func (c *shellCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "shell",
		Description:    "interactive PTY shell session over SSH (bidi stream)",
		Permission:     sdk.PermExecute,
		ConnectionType: "ssh",
		Streaming:      true,
	}
}

func (c *shellCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}

func (c *shellCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	conn := s.Connection()
	if conn == nil {
		return sdk.ErrConnectionRequired
	}

	var p shellParams
	if err := s.Params(&p); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	if p.Term == "" {
		p.Term = "xterm-256color"
	}
	if p.Rows <= 0 {
		p.Rows = 24
	}
	if p.Cols <= 0 {
		p.Cols = 80
	}

	dt, err := resolveTarget(conn)
	if err != nil {
		return sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	if dt.Creds.KnownHostsPath == "" && c.plugin != nil {
		dt.Creds.KnownHostsPath = c.plugin.defaultKnownHostsPath()
	}

	client, key, err := c.pool.Acquire(ctx, dt)
	if err != nil {
		return sdk.NewError("SSH_DIAL_FAILED", err.Error(), err).WithRetryable(true)
	}
	defer c.pool.Release(key)

	session, err := client.NewSession()
	if err != nil {
		c.pool.Evict(key)
		return sdk.NewError("SSH_SESSION_FAILED", err.Error(), err).WithRetryable(true)
	}
	defer session.Close()

	// 尽力设置环境变量：某些 sshd 配置会拒绝，不视为致命错误。
	for k, v := range p.Env {
		_ = session.Setenv(k, v)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty(p.Term, p.Rows, p.Cols, modes); err != nil {
		return sdk.NewError("SSH_PTY_FAILED", err.Error(), err)
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return sdk.NewError("SSH_STDIN_FAILED", err.Error(), err)
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return sdk.NewError("SSH_STDOUT_FAILED", err.Error(), err)
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return sdk.NewError("SSH_STDERR_FAILED", err.Error(), err)
	}

	if err := session.Shell(); err != nil {
		return sdk.NewError("SSH_SHELL_FAILED", err.Error(), err)
	}

	// ---- 输出泵：stdout / stderr → sdk.Stream ----
	var outWG sync.WaitGroup
	outWG.Add(2)
	go pumpToStream(&outWG, stdoutPipe, s.Stdout)
	go pumpToStream(&outWG, stderrPipe, s.Stderr)

	// ---- 输入泵：sdk.Stream.Recv() → stdin / signal / resize ----
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-s.Recv():
				if !ok {
					// 客户端已关闭输入端；关闭远端 stdin 让 shell 感知 EOF。
					_ = stdinPipe.Close()
					return
				}
				switch v := msg.(type) {
				case *sdk.Stdin:
					if _, werr := stdinPipe.Write(v.Data); werr != nil {
						return
					}
				case *sdk.Signal:
					switch v.Type {
					case sdk.SignalWinch:
						var wp winchPayload
						if len(v.Payload) > 0 {
							_ = json.Unmarshal(v.Payload, &wp)
						}
						if wp.Rows > 0 && wp.Cols > 0 {
							_ = session.WindowChange(wp.Rows, wp.Cols)
						}
					case sdk.SignalInt:
						_ = session.Signal(ssh.SIGINT)
					case sdk.SignalTerm:
						_ = session.Signal(ssh.SIGTERM)
					case sdk.SignalKill, sdk.SignalCancel:
						_ = session.Signal(ssh.SIGKILL)
						_ = session.Close()
						return
					}
				}
			}
		}
	}()

	// ---- ctx 取消 → 关闭 session ----
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-ctxDone:
		}
	}()

	// ---- 等待远端 shell 结束 ----
	exitCode := 0
	waitErr := session.Wait()
	if waitErr != nil {
		var ee *ssh.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitStatus()
		} else if ctx.Err() != nil {
			// ctx 取消导致的连接断开：返回 CANCELED。
			_ = stdinPipe.Close()
			outWG.Wait()
			return sdk.NewError("CANCELED", ctx.Err().Error(), ctx.Err())
		} else {
			// EOF / MissingExitStatus 等：把 shell 视为已结束，exitCode=0。
			// 其他罕见错误也不吞——透传给客户端便于排查。
			var missing *ssh.ExitMissingError
			if !errors.As(waitErr, &missing) && !errors.Is(waitErr, io.EOF) {
				_ = stdinPipe.Close()
				outWG.Wait()
				return sdk.NewError("SSH_SHELL_FAILED", waitErr.Error(), waitErr)
			}
		}
	}

	_ = stdinPipe.Close()
	outWG.Wait()
	return s.Finish(shellResult{ExitCode: exitCode}, exitCode)
}

// pumpToStream 从远端 reader 读取，转调 send（s.Stdout / s.Stderr）。
// reader EOF 或 send 失败即退出。
func pumpToStream(wg *sync.WaitGroup, r io.Reader, send func([]byte) error) {
	defer wg.Done()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// 拷贝一份，避免与后续 Read 的缓冲区竞争。
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if serr := send(chunk); serr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
