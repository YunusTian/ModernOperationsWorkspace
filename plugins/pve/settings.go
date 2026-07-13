// settings.go —— PVE 插件的 settings 解析与 endpoint 解析。
//
// settings JSON 结构与 plugin.json.settingsSchema 完全对齐：
//
//	{
//	  "endpoints": [
//	    {"name":"lab", "host":"https://pve.example.com:8006",
//	     "token_id":"root@pam!mow-read", "token_secret":"<secret>",
//	     "token_secret_env":"PVE_TOKEN", "insecure_tls":false, "timeout_seconds":30}
//	  ]
//	}
//
// token_secret 与 token_secret_env 是"任选其一"：
//   - token_secret 直接落 sidecar，Init 时通过 secret store merge 回 Settings
//   - token_secret_env 则在 Init 时读环境变量（不落磁盘，最适合 CI / Kubernetes 场景）
//   - 两者都给时以 token_secret 优先，保证配置显式性
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// endpointSettings 是配置文件里单个 endpoint 的原始结构。
type endpointSettings struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	TokenID        string `json:"token_id,omitempty"`
	TokenSecret    string `json:"token_secret,omitempty"`
	TokenSecretEnv string `json:"token_secret_env,omitempty"`
	InsecureTLS    bool   `json:"insecure_tls,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// pveSettings 是 Init 反序列化的完整 settings。
type pveSettings struct {
	Endpoints []endpointSettings `json:"endpoints,omitempty"`
}

// endpoint 是解析后可直接使用的 endpoint 视图。
// token 字段是内存态的明文，永远不会通过日志 / 审计 / RPC 输出。
type endpoint struct {
	Name        string
	BaseURL     string
	TokenID     string
	tokenSecret string
	InsecureTLS bool
	Timeout     time.Duration
}

// parseSettings 反序列化 settings JSON。任何 JSON 层错误都会连锁抛出。
func parseSettings(raw json.RawMessage) (pveSettings, error) {
	var s pveSettings
	if len(raw) == 0 {
		return s, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return pveSettings{}, err
	}
	return s, nil
}

// resolveEndpoints 把配置层结构转换成运行时视图。
//   - name 全局唯一
//   - host 必须是可解析的 URL；scheme=http/https
//   - token_secret 与 token_secret_env 二选一（可都为空，命令会在需要 auth 时报错）
//   - lookup 用于测试注入；正常路径传 os.LookupEnv
//
// 返回 (byName, defaultName, error)：defaultName 是列表中的第一个（保持声明顺序）。
func resolveEndpoints(list []endpointSettings, lookup func(string) (string, bool)) (map[string]*endpoint, string, error) {
	if len(list) == 0 {
		return map[string]*endpoint{}, "", nil
	}
	out := make(map[string]*endpoint, len(list))
	var defaultName string
	for i, e := range list {
		if e.Name == "" {
			return nil, "", fmt.Errorf("endpoints[%d].name is required", i)
		}
		if e.Host == "" {
			return nil, "", fmt.Errorf("endpoints[%d] (%s): host is required", i, e.Name)
		}
		u, err := url.Parse(e.Host)
		if err != nil {
			return nil, "", fmt.Errorf("endpoints[%d] (%s): invalid host: %w", i, e.Name, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, "", fmt.Errorf("endpoints[%d] (%s): host scheme must be http/https", i, e.Name)
		}
		if _, exists := out[e.Name]; exists {
			return nil, "", fmt.Errorf("endpoints[%d]: duplicate name %q", i, e.Name)
		}
		secret := e.TokenSecret
		if secret == "" && e.TokenSecretEnv != "" && lookup != nil {
			if v, ok := lookup(e.TokenSecretEnv); ok {
				secret = v
			}
		}
		timeout := time.Duration(e.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		out[e.Name] = &endpoint{
			Name:        e.Name,
			BaseURL:     strings.TrimRight(u.String(), "/"),
			TokenID:     e.TokenID,
			tokenSecret: secret,
			InsecureTLS: e.InsecureTLS,
			Timeout:     timeout,
		}
		if defaultName == "" {
			defaultName = e.Name
		}
	}
	return out, defaultName, nil
}
