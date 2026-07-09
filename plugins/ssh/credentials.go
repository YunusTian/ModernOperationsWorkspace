package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/mow/mow/sdk"
)

// sshCredentials 是插件内解码 sdk.Connection.Credentials 的结构。
// 它与 core/connection.SSHCredentials 的 JSON 形态保持一致，
// 但插件层不能直接依赖 core 包（保持"插件不引用 Core"的约束）。
type sshCredentials struct {
	Method         string `json:"method"`
	Password       string `json:"password,omitempty"`
	PrivateKey     string `json:"private_key,omitempty"`
	Passphrase     string `json:"passphrase,omitempty"`
	KnownHostsMode string `json:"known_hosts_mode,omitempty"`
	KnownHostsPath string `json:"known_hosts_path,omitempty"`
}

// dialTarget 汇总一次连接需要的所有参数。
type dialTarget struct {
	ID    string
	Host  string
	Port  int
	User  string
	Creds sshCredentials
	Meta  map[string]string
}

// resolveTarget 从 sdk.Connection 抽取 host / port / user / creds。
// 参数缺失时返回明确错误，Command 应把它包装为 sdk.Error 返回给 Core。
func resolveTarget(conn *sdk.Connection) (*dialTarget, error) {
	if conn == nil {
		return nil, errors.New("connection is nil")
	}
	if conn.Type != "ssh" {
		return nil, fmt.Errorf("expected connection type ssh, got %q", conn.Type)
	}
	dt := &dialTarget{
		ID:   conn.ID,
		Host: conn.Metadata["host"],
		User: conn.Metadata["user"],
		Meta: conn.Metadata,
	}
	if dt.Host == "" {
		return nil, errors.New("connection.metadata.host is empty")
	}
	if dt.User == "" {
		return nil, errors.New("connection.metadata.user is empty")
	}
	if p := conn.Metadata["port"]; p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: %w", p, err)
		}
		dt.Port = n
	}
	if dt.Port <= 0 {
		dt.Port = 22
	}

	if len(conn.Credentials) > 0 {
		if err := json.Unmarshal(conn.Credentials, &dt.Creds); err != nil {
			return nil, fmt.Errorf("decode ssh credentials: %w", err)
		}
	}
	return dt, nil
}
