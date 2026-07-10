package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mow/mow/sdk"
)

// dockerCredentials 是插件内解码 sdk.Connection.Credentials 的结构。
// 与 core/connection.DockerCredentials 的 JSON 形态保持一致，
// 但插件层不直接依赖 core 包（保持"插件不引用 Core"的约束）。
type dockerCredentials struct {
	Host       string `json:"host"`
	APIVersion string `json:"api_version,omitempty"`
	TLSVerify  bool   `json:"tls_verify,omitempty"`
	TLSCA      string `json:"tls_ca,omitempty"`
	TLSCert    string `json:"tls_cert,omitempty"`
	TLSKey     string `json:"tls_key,omitempty"`
}

// dialTarget 汇总一次连接需要的所有材料。
type dialTarget struct {
	Creds dockerCredentials
	Meta  map[string]string

	// scheme / netAddr 是 Host 解析结果：
	//   unix:///var/run/docker.sock → scheme=unix, netAddr=/var/run/docker.sock
	//   tcp://host:2375              → scheme=tcp,  netAddr=host:2375
	//   npipe:////./pipe/docker_engine → scheme=npipe, netAddr=//./pipe/docker_engine
	Scheme  string
	NetAddr string

	// APIVersion 为规范化后的路径前缀（"" 或 "/v1.44"）。
	APIVersion string
}

// resolveTarget 从 sdk.Connection 抽取 host / api_version / TLS 材料。
func resolveTarget(conn *sdk.Connection) (*dialTarget, error) {
	if conn == nil {
		return nil, errors.New("connection is nil")
	}
	if conn.Type != "docker" {
		return nil, fmt.Errorf("expected connection type docker, got %q", conn.Type)
	}
	dt := &dialTarget{Meta: conn.Metadata}
	if len(conn.Credentials) > 0 {
		if err := json.Unmarshal(conn.Credentials, &dt.Creds); err != nil {
			return nil, fmt.Errorf("decode docker credentials: %w", err)
		}
	}
	if dt.Creds.Host == "" {
		return nil, errors.New("docker host is empty")
	}
	scheme, addr, err := splitHost(dt.Creds.Host)
	if err != nil {
		return nil, err
	}
	dt.Scheme = scheme
	dt.NetAddr = addr
	dt.APIVersion = normalizeAPIVersion(dt.Creds.APIVersion)
	return dt, nil
}

// splitHost 把 "unix:///var/run/docker.sock" / "tcp://host:2375" 拆成 (scheme, address)。
// npipe:////./pipe/docker_engine → (npipe, //./pipe/docker_engine)
func splitHost(host string) (string, string, error) {
	// 特殊处理 npipe：Windows 命名管道路径包含反斜杠，url.Parse 处理不佳。
	if strings.HasPrefix(host, "npipe://") {
		return "npipe", strings.TrimPrefix(host, "npipe://"), nil
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", "", fmt.Errorf("invalid host %q: %w", host, err)
	}
	switch u.Scheme {
	case "unix":
		// url.Parse("unix:///var/run/docker.sock") → Path=/var/run/docker.sock
		return "unix", u.Path, nil
	case "tcp":
		if u.Host == "" {
			return "", "", fmt.Errorf("invalid tcp host %q: missing authority", host)
		}
		return "tcp", u.Host, nil
	default:
		return "", "", fmt.Errorf("unsupported host scheme %q", u.Scheme)
	}
}

// normalizeAPIVersion 把用户填的 "1.44" / "v1.44" / "" 归一化成路径前缀。
// 返回值形如 "/v1.44" 或 ""；后者表示由服务端选默认。
func normalizeAPIVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return "/" + v
}
