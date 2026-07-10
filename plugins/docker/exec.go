// exec.go —— v0.3 第三阶段：docker.exec 流式命令。
//
// 语义：
//   POST /containers/{id}/exec        创建 exec 实例（返回 exec_id）
//   POST /exec/{id}/start             启动并 upgrade 成双向流
//   POST /exec/{id}/resize?h=&w=      终端尺寸变化
//   GET  /exec/{id}/json              读取 ExitCode
//
// 双向流协议：
//   - Tty=false：与 logs 一致的 8 字节 mux 帧（stdin/stdout/stderr）
//   - Tty=true ：原始字节流；stdin/stdout 复用同一条 conn
//
// 输入：s.Recv() 传来的 *sdk.Stdin 写入 conn；*sdk.Signal(SignalWinch) → resize。
//
// 结束：
//   - 客户端主动 cancel → 关 conn → 读取 exit_code
//   - 远端结束 → 读到 io.EOF → 读取 exit_code
//   - Finish(payload, exitCode)
//
// 边界：
//   - 不支持 TLS hijack（见 client.go dialHijack 注释）
//   - 不做 Detach key 转义

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"

	"github.com/mow/mow/sdk"
)

type execParams struct {
	// ID：容器 ID / 名字。必填。
	ID string `json:"id"`
	// Cmd：要执行的命令与参数，例：["sh","-lc","ls /"]。必填、非空。
	Cmd []string `json:"cmd"`

	// User / WorkingDir：可选。
	User       string `json:"user,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	// Env：额外环境变量，形如 ["KEY=VALUE"]。
	Env []string `json:"env,omitempty"`

	// Tty：分配伪终端。true 时 stdout / stderr 合并为原始字节流。
	Tty bool `json:"tty,omitempty"`
	// AttachStdin：允许写入 stdin。默认 false。
	AttachStdin bool `json:"attach_stdin,omitempty"`
	// Detach 用户主动脱离，不影响远端进程。当前仅通过 ctx cancel 实现，无需字段。
}

// execCreateBody 是 POST /containers/{id}/exec 的请求体（局部）。
type execCreateBody struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	User         string   `json:"User,omitempty"`
}

// execCreateResp / execStartBody / execInspectResp 对齐 Engine 响应。
type execCreateResp struct {
	ID string `json:"Id"`
}

type execStartBody struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}

type execInspectResp struct {
	Running  bool `json:"Running"`
	ExitCode int  `json:"ExitCode"`
}

// execResult 是最终 Finish 携带的 finalData。
type execResult struct {
	ExecID   string `json:"exec_id"`
	Tty      bool   `json:"tty"`
	ExitCode int    `json:"exit_code"`
}

// winchPayload 是 SignalWinch 事件的负载：{ rows, cols }。
// 与 plugins/ssh 保持一致的字段命名。
type winchPayload struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type execCmd struct{}

func (c *execCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "exec",
		Description:    "run a command inside a running Docker container (bidirectional stream)",
		Permission:     sdk.PermExecute,
		ConnectionType: "docker",
		Streaming:      true,
	}
}

func (c *execCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}

func (c *execCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	conn := s.Connection()
	if conn == nil {
		return sdk.ErrConnectionRequired
	}
	var p execParams
	if err := s.Params(&p); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	if p.ID == "" {
		return sdk.NewError("PARAM_INVALID", "id is required", nil)
	}
	if len(p.Cmd) == 0 {
		return sdk.NewError("PARAM_INVALID", "cmd must be a non-empty array", nil)
	}

	dt, err := resolveTarget(conn)
	if err != nil {
		return sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}

	// v0.3.1 硬护栏：exec 只支持 unix / plain tcp / tcp+TLS / (Windows) npipe。
	//   - TLS：dialHijack 在 raw conn 之上做 tls.HandshakeContext，已在 v0.3.1 打通
	//   - npipe：Windows 版本已经通过 winio.DialPipeContext 支持；其它平台在
	//     newEngineClient 处即返回 DOCKER_NPIPE_UNSUPPORTED，此处不再重复。
	// 前端 UI 依据 DescribeDockerTarget 决定按钮可用性。
	// 详见：docs/docker-plugin.md §4 传输协议 与 §12.4 exec 安全边界。
	if dt.Scheme == "npipe" && !npipeSupported {
		return sdk.NewError("DOCKER_EXEC_NPIPE_UNSUPPORTED",
			"docker.exec over Windows named pipe is only available on Windows", nil)
	}

	cli, err := newEngineClient(dt)
	if err != nil {
		return sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	// 1) create exec
	createBody := execCreateBody{
		AttachStdin:  p.AttachStdin,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          p.Tty,
		Cmd:          p.Cmd,
		Env:          p.Env,
		WorkingDir:   p.WorkingDir,
		User:         p.User,
	}
	var created execCreateResp
	if err := cli.postJSON(ctx, "/containers/"+url.PathEscape(p.ID)+"/exec", nil, createBody, &created); err != nil {
		return err
	}
	if created.ID == "" {
		return sdk.NewError("DOCKER_ENGINE_ERROR", "exec create returned empty id", nil)
	}

	// 2) start via hijacked connection
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	netConn, err := cli.dialHijack(streamCtx, "/exec/"+created.ID+"/start", nil,
		execStartBody{Detach: false, Tty: p.Tty})
	if err != nil {
		return err
	}
	defer netConn.Close()

	// 3) 转发信号 / stdin / 尺寸变化
	go relayInbound(streamCtx, s, netConn, cli, created.ID, p.AttachStdin, p.Tty, cancel)

	// 4) 读循环：ttys=false 走 mux；ttys=true 直接透传
	var readErr error
	if p.Tty {
		readErr = pumpRaw(streamCtx, netConn, s)
	} else {
		readErr = pumpMux(streamCtx, netConn, s)
	}

	// 5) 关连接（若还未关），拉 exit code
	_ = netConn.Close()
	code, inspectErr := fetchExecExit(ctx, cli, created.ID)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		// 读循环出错优先返回，方便定位；exit code 记进 details。
		if e, ok := readErr.(*sdk.Error); ok {
			return e.WithDetails(map[string]any{"exec_id": created.ID, "exit_code": code})
		}
		return readErr
	}
	if inspectErr != nil {
		return inspectErr
	}
	return s.Finish(execResult{ExecID: created.ID, Tty: p.Tty, ExitCode: code}, code)
}

// relayInbound 把 stream.Recv 上的 Stdin / SignalWinch 转成 netConn.Write / resize API。
func relayInbound(
	ctx context.Context, s sdk.Stream, netConn net.Conn,
	cli *engineClient, execID string, attachStdin, tty bool,
	cancel context.CancelFunc,
) {
	var writeMu sync.Mutex
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-s.Recv():
			if !ok {
				return
			}
			switch v := msg.(type) {
			case *sdk.Stdin:
				if !attachStdin {
					continue // 静默丢弃：exec 未 attach_stdin，用户误发也不算错误
				}
				writeMu.Lock()
				_, _ = netConn.Write(v.Data)
				writeMu.Unlock()
			case *sdk.Signal:
				switch v.Type {
				case sdk.SignalCancel, sdk.SignalInt, sdk.SignalTerm, sdk.SignalKill:
					cancel()
					return
				case sdk.SignalWinch:
					if !tty {
						continue
					}
					var wp winchPayload
					if err := json.Unmarshal(v.Payload, &wp); err != nil || wp.Rows <= 0 || wp.Cols <= 0 {
						continue
					}
					// Resize 是 fire-and-forget，独立超时；失败不影响主流。
					go doResize(ctx, cli, execID, wp.Rows, wp.Cols)
				}
			}
		}
	}
}

// doResize 调用 POST /exec/{id}/resize?h=&w=。
// Engine 不响应 body；忽略 4xx/5xx（尺寸设置失败不该打断流）。
func doResize(ctx context.Context, cli *engineClient, execID string, rows, cols int) {
	q := url.Values{}
	q.Set("h", intToStr(rows))
	q.Set("w", intToStr(cols))
	_ = cli.postNoBody(ctx, "/exec/"+execID+"/resize", q)
}

// fetchExecExit 拉 exit code；exec 运行中会拿到 Running=true，退化为 -1。
func fetchExecExit(ctx context.Context, cli *engineClient, execID string) (int, error) {
	var body execInspectResp
	if err := cli.getJSON(ctx, "/exec/"+execID+"/json", nil, &body); err != nil {
		return -1, err
	}
	if body.Running {
		return -1, nil
	}
	return body.ExitCode, nil
}

func intToStr(n int) string {
	// 小型 helper：避免多引一次 strconv 让 diff 更集中；此处 n 一定为正。
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
