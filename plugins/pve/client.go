// client.go —— PVE HTTP 客户端。
//
// 认证：Proxmox VE API Token (`Authorization: PVEAPIToken=<id>=<secret>`)。
// 我们只支持 token，不支持 ticket-based session（后者需要 CSRF token +
// cookie，属于 v0.7 正式版的范围）。
//
// 错误映射：
//   - HTTP 401/403 → PVE_UNAUTHORIZED
//   - HTTP 404 → PVE_NOT_FOUND
//   - 5xx → PVE_UPSTREAM
//   - 连接错误 → PVE_UNREACHABLE
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mow/mow/sdk"
)

// httpClient 是 endpoint 的运行时 HTTP 客户端。
type httpClient struct {
	base *http.Client
	ep   *endpoint
}

func newHTTPClient(ep *endpoint) *httpClient {
	// InsecureSkipVerify 是用户显式为自建实验环境（自签证书）开启的选项，
	// 默认关闭；MinVersion 固定为 TLS 1.2 以避免弱协议。
	tlsCfg := &tls.Config{ //nolint:gosec // G402: InsecureSkipVerify 由用户显式配置
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: ep.InsecureTLS, // #nosec G402 -- opt-in for self-signed labs
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg}
	return &httpClient{
		base: &http.Client{Timeout: ep.Timeout, Transport: tr},
		ep:   ep,
	}
}

// authorized 检查 token id/secret 是否已完整；命令在需要写操作时先调用。
func (c *httpClient) authorized() bool {
	return c.ep.TokenID != "" && c.ep.tokenSecret != ""
}

// getJSON 拉取 `/api2/json<path>?<query>` 并把 `.data` 反序列化到 out。
func (c *httpClient) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	return c.request(ctx, http.MethodGet, path, query, nil, out)
}

// post 发送 `/api2/json<path>` 表单请求（PVE 大部分变更 API 使用 form-encoded）。
// PVE 的写接口通常返回一个字符串（task UPID）；out 可传 *string 拿到该值。
func (c *httpClient) post(ctx context.Context, path string, form url.Values, out any) error {
	return c.request(ctx, http.MethodPost, path, nil, form, out)
}

func (c *httpClient) request(ctx context.Context, method, path string, query url.Values, form url.Values, out any) error {
	if !c.authorized() {
		return sdk.NewError("PVE_UNAUTHORIZED", "PVE api token not configured (token_id / token_secret)", nil)
	}
	full := c.ep.BaseURL + "/api2/json" + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return sdk.NewError("PVE_REQUEST_INVALID", err.Error(), err)
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.ep.TokenID+"="+c.ep.tokenSecret)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.base.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return sdk.NewError("PVE_UNREACHABLE", uerr.Err.Error(), err)
		}
		return sdk.NewError("PVE_UNREACHABLE", err.Error(), err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return sdk.NewError("PVE_UPSTREAM", "read response: "+err.Error(), err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return sdk.NewError("PVE_UNAUTHORIZED", classifyBody(resp.StatusCode, raw), nil)
	}
	if resp.StatusCode == http.StatusNotFound {
		return sdk.NewError("PVE_NOT_FOUND", classifyBody(resp.StatusCode, raw), nil)
	}
	if resp.StatusCode >= 500 {
		return sdk.NewError("PVE_UPSTREAM", classifyBody(resp.StatusCode, raw), nil)
	}
	if resp.StatusCode >= 400 {
		return sdk.NewError("PVE_BAD_REQUEST", classifyBody(resp.StatusCode, raw), nil)
	}
	if out == nil {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return sdk.NewError("PVE_UPSTREAM", "decode envelope: "+err.Error(), err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return sdk.NewError("PVE_DECODE_FAILED", err.Error(), err)
	}
	return nil
}

// envelope 描述 PVE `/api2/json/*` 的通用外层结构：`{"data": ..., "errors": ...}`
type envelope struct {
	Data   json.RawMessage `json:"data"`
	Errors json.RawMessage `json:"errors,omitempty"`
}

func classifyBody(status int, body []byte) string {
	trim := strings.TrimSpace(string(body))
	if trim == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	// PVE 的错误 body 一般是 `{"data":null,"errors":{...}}` 或纯字符串。
	// 只做最简单的裁剪，避免把很长的 HTML 页面塞进错误信息。
	if len(trim) > 512 {
		trim = trim[:512]
	}
	return fmt.Sprintf("HTTP %d: %s", status, trim)
}
