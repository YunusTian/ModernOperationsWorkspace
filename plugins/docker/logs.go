package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// docker.logs —— 流式日志
// -----------------------------------------------------------------------------
//
// Docker Engine API：GET /containers/{id}/logs?stdout=1&stderr=1&follow=1&tail=100&timestamps=1
//
// 语义：
//   - Follow=false 时读取历史日志一次性返回 exit
//   - Follow=true  时持续推送，直到 ctx 取消 / 客户端 Cancel / 容器结束
//   - Tty=true 的容器：body 是原始字节；tty=false（默认）：body 是 8 字节头帧
//     复用流（stdout / stderr 分帧）
//
// 输出：
//   - stdout 帧 → s.Stdout
//   - stderr 帧 → s.Stderr
//   - 结束时 s.Finish({follow, tty}, 0)

type logsParams struct {
	ID         string `json:"id"`
	Follow     bool   `json:"follow,omitempty"`
	Tail       string `json:"tail,omitempty"`       // "all" 或数字字符串；默认 "100"
	Stdout     *bool  `json:"stdout,omitempty"`     // 默认 true
	Stderr     *bool  `json:"stderr,omitempty"`     // 默认 true
	Timestamps bool   `json:"timestamps,omitempty"` // 是否在每行前加时间戳
	Since      int64  `json:"since,omitempty"`      // Unix 秒；0 忽略
	Until      int64  `json:"until,omitempty"`      // Unix 秒；0 忽略

	// Tty 手工指示容器是否以 tty 模式启动：
	// - true → 直接透传字节
	// - false（默认）→ 走 8 字节 mux 头解码
	// v0.3 MVP 不自动 inspect，由调用方声明。
	Tty bool `json:"tty,omitempty"`
}

type logsResult struct {
	Follow bool `json:"follow"`
	Tty    bool `json:"tty"`
}

type logsCmd struct{}

func (c *logsCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "logs",
		Description:    "stream Docker container logs (stdout/stderr, optional follow)",
		Permission:     sdk.PermRead,
		ConnectionType: "docker",
		Streaming:      true,
	}
}

func (c *logsCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}

func (c *logsCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	conn := s.Connection()
	if conn == nil {
		return sdk.ErrConnectionRequired
	}
	var p logsParams
	if err := s.Params(&p); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	if p.ID == "" {
		return sdk.NewError("PARAM_INVALID", "id is required", nil)
	}

	dt, err := resolveTarget(conn)
	if err != nil {
		return sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	q := url.Values{}
	stdout := true
	if p.Stdout != nil {
		stdout = *p.Stdout
	}
	stderr := true
	if p.Stderr != nil {
		stderr = *p.Stderr
	}
	if !stdout && !stderr {
		return sdk.NewError("PARAM_INVALID", "at least one of stdout / stderr must be true", nil)
	}
	if stdout {
		q.Set("stdout", "1")
	}
	if stderr {
		q.Set("stderr", "1")
	}
	if p.Follow {
		q.Set("follow", "1")
	}
	tail := p.Tail
	if tail == "" {
		tail = "100"
	}
	q.Set("tail", tail)
	if p.Timestamps {
		q.Set("timestamps", "1")
	}
	if p.Since > 0 {
		q.Set("since", strconv.FormatInt(p.Since, 10))
	}
	if p.Until > 0 {
		q.Set("until", strconv.FormatInt(p.Until, 10))
	}

	// 流式监听 s.Recv 中的取消信号，映射到 ctx.cancel。
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchSignals(streamCtx, s, cancel)

	resp, err := cli.do(streamCtx, http.MethodGet, "/containers/"+url.PathEscape(p.ID)+"/logs", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if p.Tty {
		// TTY 容器：原始字节流，无法区分 stdout / stderr，全部当作 stdout 输出。
		if err := pumpRaw(streamCtx, resp.Body, s); err != nil {
			return err
		}
	} else {
		if err := pumpMux(streamCtx, resp.Body, s); err != nil {
			return err
		}
	}
	return s.Finish(logsResult{Follow: p.Follow, Tty: p.Tty}, 0)
}

// pumpRaw 把原始字节流全部当 stdout 推给 sdk.Stream。
func pumpRaw(ctx context.Context, r io.Reader, s sdk.Stream) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if serr := s.Stdout(chunk); serr != nil {
				return serr
			}
		}
		if err != nil {
			return mapReadErr(ctx, err)
		}
	}
}

// pumpMux 按 8 字节头解码 stdout / stderr 分帧，转发到 sdk.Stream。
func pumpMux(ctx context.Context, r io.Reader, s sdk.Stream) error {
	mr := newMuxReader(r)
	for {
		kind, chunk, err := mr.nextFrame()
		if err != nil {
			return mapReadErr(ctx, err)
		}
		if len(chunk) == 0 {
			continue
		}
		// nextFrame 会复用底层缓冲，先拷贝再发送
		payload := make([]byte, len(chunk))
		copy(payload, chunk)

		switch kind {
		case stdStdout, stdStdin:
			if err := s.Stdout(payload); err != nil {
				return err
			}
		case stdStderr:
			if err := s.Stderr(payload); err != nil {
				return err
			}
		}
	}
}

// mapReadErr 把读循环里的错误转为对外错误：
//   - io.EOF → nil（正常结束）
//   - ctx.Err → CANCELED / TIMEOUT
//   - 其余 → DOCKER_READ_FAILED
func mapReadErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return sdk.NewError("TIMEOUT", ctx.Err().Error(), ctx.Err())
		}
		return sdk.NewError("CANCELED", ctx.Err().Error(), ctx.Err())
	}
	return sdk.NewError("DOCKER_READ_FAILED", err.Error(), err)
}

// watchSignals 监听 sdk.Stream 的入站消息：任何 Cancel / Term / Kill 信号都会
// 触发 cancel，从而中止底层 HTTP 请求。
// Stdin 在 logs 场景没有意义，直接丢弃。
func watchSignals(ctx context.Context, s sdk.Stream, cancel context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-s.Recv():
			if !ok {
				return
			}
			if sig, ok := msg.(*sdk.Signal); ok {
				switch sig.Type {
				case sdk.SignalCancel, sdk.SignalInt, sdk.SignalTerm, sdk.SignalKill:
					cancel()
					return
				}
			}
		}
	}
}
