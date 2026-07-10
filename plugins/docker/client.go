package main

import (
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
