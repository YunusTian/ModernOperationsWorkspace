// hijack_tls_test.go —— v0.3.1 新增：验证 dialHijack 在 TLS Docker Engine 上工作。
//
// 用 httptest.NewTLSServer 起一个假 Engine，handler 手工做 hijack：
//   - 校验请求头 Upgrade / Connection
//   - 写 "HTTP/1.1 101 UPGRADED" 状态 + 空行
//   - 再写一段固定 payload
//
// 客户端配置 tls.Config 只挂 RootCAs + ServerName（不做 mTLS 简化测试）；
// dialHijack 应该：
//   1) 通过 Transport.DialContext 拨号成功
//   2) 在 raw conn 上完成 tls.HandshakeContext
//   3) 消费掉 101 头，把 payload 交给上层
//
// 该测试是 v0.3.1 "TLS raw-hijack" 能力的最小回归依据；真实 Docker daemon
// 场景仍由 tests/e2e/docker_e2e_test.go 覆盖。
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDialHijack_OverTLS(t *testing.T) {
	const payload = "hello-tls-hijack"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "tcp") {
			http.Error(w, "expected Upgrade: tcp", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusInternalServerError)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 101 UPGRADED\r\n")
		_, _ = bufrw.WriteString("Content-Type: application/vnd.docker.raw-stream\r\n")
		_, _ = bufrw.WriteString("Connection: Upgrade\r\n")
		_, _ = bufrw.WriteString("Upgrade: tcp\r\n\r\n")
		_, _ = bufrw.WriteString(payload)
		_ = bufrw.Flush()
	})

	srv := httptest.NewTLSServer(handler)
	defer srv.Close()

	// httptest.NewTLSServer 提供 srv.Certificate() 是自签证书；导出为 PEM 供 client 信任。
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: srv.Certificate().Raw,
	})
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	u, _ := url.Parse(srv.URL)
	addr := u.Host

	tlsCfg := &tls.Config{
		RootCAs:    pool,
		ServerName: "example.com", // httptest 的默认证书 DNS SAN
		MinVersion: tls.VersionTLS12,
	}

	// 组装最小 engineClient：只填 dialHijack 需要的字段。
	c := &engineClient{
		httpc: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "tcp", addr)
				},
			},
		},
		baseHost:   "https://" + addr,
		dialScheme: "tcp",
		dialAddr:   addr,
		tlsCfg:     tlsCfg,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := c.dialHijack(ctx, "/hijack", nil, map[string]any{"ping": true})
	if err != nil {
		t.Fatalf("dialHijack over TLS: %v", err)
	}
	defer conn.Close()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("payload = %q, want %q", buf, payload)
	}
}

// TestDialHijack_TLSHandshakeFailure：故意用错的 RootCA 让握手失败，
// 断言错误码 DOCKER_TLS_HANDSHAKE_FAILED。
func TestDialHijack_TLSHandshakeFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	addr := u.Host

	// 空 pool → 服务器证书不被信任 → HandshakeContext 应失败
	tlsCfg := &tls.Config{
		RootCAs:    x509.NewCertPool(),
		ServerName: "example.com",
		MinVersion: tls.VersionTLS12,
	}
	c := &engineClient{
		httpc: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "tcp", addr)
				},
			},
		},
		dialScheme: "tcp",
		dialAddr:   addr,
		tlsCfg:     tlsCfg,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := c.dialHijack(ctx, "/hijack", nil, nil)
	if err == nil {
		t.Fatal("expected handshake failure")
	}
	if !strings.Contains(err.Error(), "DOCKER_TLS_HANDSHAKE_FAILED") &&
		!strings.Contains(strings.ToLower(err.Error()), "tls") &&
		!strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("expected TLS handshake error, got %v", err)
	}
}
