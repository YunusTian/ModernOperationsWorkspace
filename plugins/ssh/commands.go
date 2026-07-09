package main

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// ssh.ping —— 端到端验证用
// -----------------------------------------------------------------------------

type pingCmd struct{}

func (c *pingCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          "ping",
		Description: "returns pong; used for grpcbridge sanity check",
		Permission:  sdk.PermRead,
	}
}

func (c *pingCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	data, _ := json.Marshal(map[string]string{"pong": "ok"})
	return &sdk.ExecuteResponse{Data: data}, nil
}

func (c *pingCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

// -----------------------------------------------------------------------------
// ssh.exec —— 一次性远端命令执行
// -----------------------------------------------------------------------------

// execParams 是 ssh.exec 的入参。
type execParams struct {
	// Cmd 是要在远端执行的完整 shell 命令行（不做本地解析）。
	Cmd string `json:"cmd"`

	// Stdin 是可选的标准输入（例如 heredoc 场景）。
	Stdin string `json:"stdin,omitempty"`
}

// execResult 是 ssh.exec 的输出。
type execResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// execCmd 是 ssh.exec 的实现。
// 权限：Execute（等价于用户远程手敲一条命令）。
type execCmd struct {
	pool *SessionPool
}

func (c *execCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "exec",
		Description:    "execute a one-shot command on the remote host via SSH",
		Permission:     sdk.PermExecute,
		ConnectionType: "ssh",
	}
}

func (c *execCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func (c *execCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}

	var p execParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, sdk.NewError("PARAM_INVALID", "decode params failed", err)
		}
	}
	if p.Cmd == "" {
		return nil, sdk.NewError("PARAM_INVALID", "cmd is required", nil)
	}

	dt, err := resolveTarget(req.Connection)
	if err != nil {
		return nil, sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}

	client, key, err := c.pool.Acquire(ctx, dt)
	if err != nil {
		return nil, sdk.NewError("SSH_DIAL_FAILED", err.Error(), err).WithRetryable(true)
	}
	defer c.pool.Release(key)

	session, err := client.NewSession()
	if err != nil {
		// 复用的 client 可能已经断开：evict 后调用方可重试。
		c.pool.Evict(key)
		return nil, sdk.NewError("SSH_SESSION_FAILED", err.Error(), err).WithRetryable(true)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if p.Stdin != "" {
		session.Stdin = bytes.NewReader([]byte(p.Stdin))
	}

	// 将 ctx cancel 映射到 session：ctx 结束时关闭 session。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
		}
	}()

	exitCode := 0
	if err := session.Run(p.Cmd); err != nil {
		if ee, ok := err.(interface{ ExitStatus() int }); ok {
			exitCode = ee.ExitStatus()
		} else if ctx.Err() != nil {
			return nil, sdk.NewError("CANCELED", ctx.Err().Error(), ctx.Err())
		} else {
			return nil, sdk.NewError("SSH_EXEC_FAILED", err.Error(), err)
		}
	}

	data, err := json.Marshal(execResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	})
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}
