package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// engineClient —— Docker Engine HTTP 客户端
// -----------------------------------------------------------------------------
//
// 走 Docker Remote API：
//   - unix socket / tcp / tcp+TLS
//   - 不引入官方 docker/docker SDK（依赖体积过大），只用标准库 net/http
//
// 每次 Command 调用创建一个 engineClient，无需连接池：底层 Transport 自带
// keep-alive；MVP 阶段一次性命令 + 单次 log stream 已经够用。

type engineClient struct {
	httpc      *http.Client
	baseHost   string // "http://docker" 之类的占位 authority，实际拨号忽略之
	apiPrefix  string // 例："/v1.44"，可能为空
	dialScheme string // unix / tcp / npipe
	dialAddr   string
}

func newEngineClient(dt *dialTarget) (*engineClient, error) {
	if dt == nil {
		return nil, errors.New("engineClient: dialTarget is nil")
	}
	tr := &http.Transport{
		DisableCompression: true, // 日志流不需要 gzip
		IdleConnTimeout:    30 * time.Second,
	}

	switch dt.Scheme {
	case "unix":
		addr := dt.NetAddr
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", addr)
		}
	case "tcp":
		addr := dt.NetAddr
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}
		if dt.Creds.TLSVerify || dt.Creds.TLSCA != "" {
			tlsConf, err := buildTLSConfig(&dt.Creds, dt.NetAddr)
			if err != nil {
				return nil, err
			}
			tr.TLSClientConfig = tlsConf
		}
	case "npipe":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("npipe is supported only on windows")
		}
		return nil, fmt.Errorf("npipe transport not implemented in this MVP; use tcp:// or unix://")
	default:
		return nil, fmt.Errorf("unsupported scheme %q", dt.Scheme)
	}

	c := &engineClient{
		httpc: &http.Client{
			Transport: tr,
			// 不设整体超时——日志流是长连接；每个 Command 通过 ctx 控制。
		},
		dialScheme: dt.Scheme,
		dialAddr:   dt.NetAddr,
		apiPrefix:  dt.APIVersion,
	}

	// baseHost：unix / npipe 时用占位符 "docker"；tcp 时用真实 host:port。
	if dt.Scheme == "tcp" {
		if dt.Creds.TLSVerify || dt.Creds.TLSCA != "" {
			c.baseHost = "https://" + dt.NetAddr
		} else {
			c.baseHost = "http://" + dt.NetAddr
		}
	} else {
		c.baseHost = "http://docker" // Transport.DialContext 会覆盖真实拨号目的地
	}
	return c, nil
}

// closeIdle 关闭底层 Transport 的空闲连接；Command 完成后调用。
func (c *engineClient) closeIdle() {
	if tr, ok := c.httpc.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

// dialHijack 建立一条**独占**的 TCP / unix 连接，向 Engine 发送 POST + Upgrade 请求，
// 消费掉 101 Switching Protocols 头，然后把 net.Conn 直接返回给调用方做双向流。
//
// 用于 docker.exec 场景：/exec/{id}/start 走 raw multiplexed stream，
// Go 的 net/http 无法在 200 OK 后拿到底层连接（Response.Body 只读），因此
// 手工发协议。
func (c *engineClient) dialHijack(
	ctx context.Context, path string, query url.Values, body any,
) (net.Conn, error) {
	var dialer func(context.Context) (net.Conn, error)
	tr, _ := c.httpc.Transport.(*http.Transport)
	if tr == nil || tr.DialContext == nil {
		return nil, sdk.NewError("DOCKER_CLIENT_INVALID", "transport has no dialer", nil)
	}
	dialer = func(ctx context.Context) (net.Conn, error) {
		return tr.DialContext(ctx, "tcp", "docker")
	}

	conn, err := dialer(ctx)
	if err != nil {
		return nil, mapTransportError(err)
	}
	// TLS 场景需要在 raw conn 之上再握手；MVP 阶段 exec 只支持 tcp / unix 明文。
	// 若走 TLS，需要引入 tls.Client(conn, tlsCfg).Handshake()——当前跳过。
	// buildRequest 与 do 保持一致（无 apiPrefix 会被 Engine 拒），沿用 apiPrefix。
	full := c.apiPrefix + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	// 手写 request：不能用 http.NewRequest+client.Do，因为需要拿回底层 conn。
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			conn.Close()
			return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
		}
		payload = raw
	}
	reqLine := fmt.Sprintf("POST %s HTTP/1.1\r\n"+
		"Host: docker\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"Upgrade: tcp\r\n"+
		"Connection: Upgrade\r\n"+
		"\r\n", full, len(payload))
	if _, err := conn.Write([]byte(reqLine)); err != nil {
		conn.Close()
		return nil, sdk.NewError("DOCKER_WRITE_FAILED", err.Error(), err)
	}
	if len(payload) > 0 {
		if _, err := conn.Write(payload); err != nil {
			conn.Close()
			return nil, sdk.NewError("DOCKER_WRITE_FAILED", err.Error(), err)
		}
	}
	// 读响应行 + 头，直到空行
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, sdk.NewError("DOCKER_READ_FAILED", "read status: "+err.Error(), err)
	}
	// 期望 "HTTP/1.1 101 UPGRADED" 或 "HTTP/1.1 200 OK"（Docker 老版本在无 upgrade 时也直接 200）
	if !strings.Contains(statusLine, " 101 ") && !strings.Contains(statusLine, " 200 ") {
		// 读完剩余 header 便于返回 message
		msg := statusLine
		for {
			line, err := br.ReadString('\n')
			if err != nil || line == "\r\n" || line == "\n" {
				break
			}
			msg += line
		}
		conn.Close()
		return nil, sdk.NewError("DOCKER_HIJACK_FAILED", "hijack failed: "+strings.TrimSpace(statusLine), nil).
			WithDetails(map[string]any{"raw": msg})
	}
	// 消费剩余 header
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, sdk.NewError("DOCKER_READ_FAILED", "read headers: "+err.Error(), err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn 让 bufio.Reader 中残留的字节仍能通过 conn.Read 读出。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// -----------------------------------------------------------------------------
// 请求工具
// -----------------------------------------------------------------------------

// do 发起一次请求；调用方负责关闭返回体。
// 返回体在错误分支中已被 do 关闭。
func (c *engineClient) do(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	full := c.baseHost + c.apiPrefix + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, full, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, mapTransportError(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeEngineError(resp)
	}
	return resp, nil
}

// getJSON 发一次 GET 并把响应体 JSON 解到 dst。
func (c *engineClient) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	resp, err := c.do(ctx, http.MethodGet, path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if dst == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// postNoBody 发一次 POST，忽略响应体（成功码 204 / 200 视为成功；
// 304 Not Modified 保留状态码语义，转成 DOCKER_NOT_MODIFIED 由调用方处理）。
func (c *engineClient) postNoBody(ctx context.Context, path string, query url.Values) error {
	resp, err := c.do(ctx, http.MethodPost, path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotModified {
		return sdk.NewError("DOCKER_NOT_MODIFIED",
			"target already in desired state",
			nil).WithDetails(map[string]any{"http_status": resp.StatusCode})
	}
	return nil
}

// doWithBody 发起一个带 body 与自定义 header 的请求。调用方负责关闭返回体。
//
// - method / path / query：见 do
// - body：nil 表示无请求体
// - contentType / headers：可选；headers 里面的键值原样合并到 Header
func (c *engineClient) doWithBody(
	ctx context.Context, method, path string, query url.Values,
	body io.Reader, contentType string, headers map[string]string,
) (*http.Response, error) {
	full := c.baseHost + c.apiPrefix + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, mapTransportError(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeEngineError(resp)
	}
	return resp, nil
}

// postJSON 发一次 POST，将 body 编码为 JSON；把响应体解到 dst（可为 nil）。
func (c *engineClient) postJSON(
	ctx context.Context, path string, query url.Values,
	body any, dst any,
) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return sdk.NewError("ENCODE_FAILED", "marshal body: "+err.Error(), err)
		}
		reader = bytes.NewReader(raw)
	}
	resp, err := c.doWithBody(ctx, http.MethodPost, path, query, reader, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if dst == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// -----------------------------------------------------------------------------
// 错误映射：Engine → sdk.Error
// -----------------------------------------------------------------------------

// engineErrorBody 是 Docker Engine 的错误体：{"message":"..."}
type engineErrorBody struct {
	Message string `json:"message"`
}

// decodeEngineError 把 4xx / 5xx 响应转为稳定的 sdk.Error。
func decodeEngineError(resp *http.Response) error {
	var body engineErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		msg = "engine error"
	}
	code := statusCodeToErrorCode(resp.StatusCode)
	err := sdk.NewError(code, msg, nil).WithDetails(map[string]any{
		"http_status": resp.StatusCode,
	})
	// 5xx 可重试；4xx 一般不可重试
	if resp.StatusCode >= 500 {
		err = err.WithRetryable(true)
	}
	return err
}

// statusCodeToErrorCode 把 HTTP 状态码映射到 MOW 错误码。
func statusCodeToErrorCode(code int) string {
	switch code {
	case http.StatusNotFound:
		return "DOCKER_NOT_FOUND"
	case http.StatusConflict:
		return "DOCKER_CONFLICT"
	case http.StatusNotModified:
		return "DOCKER_NOT_MODIFIED"
	case http.StatusBadRequest:
		return "DOCKER_BAD_REQUEST"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "DOCKER_UNAUTHORIZED"
	case http.StatusInternalServerError:
		return "DOCKER_ENGINE_ERROR"
	default:
		if code >= 500 {
			return "DOCKER_ENGINE_ERROR"
		}
		return "DOCKER_HTTP_" + strconv.Itoa(code)
	}
}

// mapTransportError 把 net/http / net.OpError 转为可重试的 SDK 错误。
func mapTransportError(err error) error {
	if err == nil {
		return nil
	}
	// context 取消 / 超时
	if errors.Is(err, context.Canceled) {
		return sdk.NewError("CANCELED", err.Error(), err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return sdk.NewError("TIMEOUT", err.Error(), err)
	}
	return sdk.NewError("DOCKER_DIAL_FAILED", err.Error(), err).WithRetryable(true)
}

// -----------------------------------------------------------------------------
// TLS
// -----------------------------------------------------------------------------

func buildTLSConfig(c *dockerCredentials, addr string) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.TLSCA)) {
		return nil, errors.New("docker: tls_ca is not a valid PEM bundle")
	}
	cert, err := tls.X509KeyPair([]byte(c.TLSCert), []byte(c.TLSKey))
	if err != nil {
		return nil, fmt.Errorf("docker: parse tls cert/key: %w", err)
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
	}
	return &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   host,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// -----------------------------------------------------------------------------
// small helpers
// -----------------------------------------------------------------------------

// readAllLimited 读取 r 全部内容，但最多允许 max 字节；超过报错。
// 目的：把 /inspect 之类的响应封装为可控体量。
func readAllLimited(r io.Reader, max int64) ([]byte, error) {
	limited := &io.LimitedReader{R: r, N: max + 1}
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("response body exceeds %d bytes", max)
	}
	return buf, nil
}
