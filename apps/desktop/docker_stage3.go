// docker_stage3.go —— Docker Dashboard 第三阶段（v0.3）：
//
//   - DockerRm：Dangerous 双重护栏（前端必须 Confirmed=true；插件层再判 Confirmed）
//   - DockerImages / DockerVolumes / DockerNetworks：只读列表
//   - DockerExec* + docker:exec:<sid>:stdout/stderr/event/exit 事件：xterm 交互式 exec
//
// 交付边界：
//   - 不做 rm 之外的破坏操作（volume prune / image rm / network rm 留 v0.4）
//   - exec 只支持 tcp / unix 明文 Engine（TLS hijack 需插件先补）
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// DockerRm —— Dangerous（不可逆）
// -----------------------------------------------------------------------------

// DockerRmInput 是 rm 弹窗的入参。
type DockerRmInput struct {
	Container string `json:"container"`
	Force     bool   `json:"force,omitempty"`
	Volumes   bool   `json:"volumes,omitempty"`
	// 前端必须显式置 true —— 与 Command Engine 的 Dangerous 中间件叠加双重保护。
	Confirmed bool `json:"confirmed"`
}

// DockerRmResult 是 rm 成功后回填给前端的信息。
type DockerRmResult struct {
	ID      string `json:"id"`
	AuditID string `json:"audit_id"`
}

// DockerRm 执行 docker.rm。
func (a *App) DockerRm(targetID string, in DockerRmInput) (*DockerRmResult, error) {
	if !in.Confirmed {
		return nil, fmt.Errorf("dashboard: refuse to run docker.rm without user confirmation")
	}
	if in.Container == "" {
		return nil, fmt.Errorf("dashboard: container is required")
	}
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 60*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{
		"id":      in.Container,
		"force":   in.Force,
		"volumes": in.Volumes,
	})
	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "rm",
		TargetID:  targetID,
		Params:    params,
		Confirmed: true, // 已经在应用层拦过一次；这里透传给 Engine 的 Confirmer
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	if err != nil {
		return nil, err
	}
	var raw struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(resp.Data, &raw)
	return &DockerRmResult{ID: raw.ID, AuditID: resp.AuditID}, nil
}

// -----------------------------------------------------------------------------
// DockerImages / Volumes / Networks —— 只读
// -----------------------------------------------------------------------------

// DockerImageVM 是 UI 需要的镜像行；字段与插件的 imageEntry 对齐。
type DockerImageVM struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id,omitempty"`
	RepoTags    []string          `json:"repo_tags,omitempty"`
	RepoDigests []string          `json:"repo_digests,omitempty"`
	Created     int64             `json:"created,omitempty"`
	Size        int64             `json:"size,omitempty"`
	VirtualSize int64             `json:"virtual_size,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Containers  int               `json:"containers,omitempty"`
}

type DockerImagesInput struct {
	All bool `json:"all,omitempty"`
}
type DockerImagesResult struct {
	Images  []DockerImageVM `json:"images"`
	AuditID string          `json:"audit_id"`
}

func (a *App) DockerImages(targetID string, in DockerImagesInput) (*DockerImagesResult, error) {
	resp, err := a.dockerReadOnly(targetID, "images", in)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Images []DockerImageVM `json:"images"`
	}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	return &DockerImagesResult{Images: raw.Images, AuditID: resp.AuditID}, nil
}

// DockerVolumeVM 是 UI 需要的卷行。
type DockerVolumeVM struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"`
	Mountpoint string            `json:"mountpoint,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	CreatedAt  string            `json:"created_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
}

type DockerVolumesResult struct {
	Volumes  []DockerVolumeVM `json:"volumes"`
	Warnings []string         `json:"warnings,omitempty"`
	AuditID  string           `json:"audit_id"`
}

func (a *App) DockerVolumes(targetID string) (*DockerVolumesResult, error) {
	resp, err := a.dockerReadOnly(targetID, "volumes", nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Volumes  []DockerVolumeVM `json:"volumes"`
		Warnings []string         `json:"warnings,omitempty"`
	}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return nil, fmt.Errorf("decode volumes: %w", err)
	}
	return &DockerVolumesResult{Volumes: raw.Volumes, Warnings: raw.Warnings, AuditID: resp.AuditID}, nil
}

// DockerNetworkVM 是 UI 需要的网络行。
type DockerNetworkVM struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Driver        string            `json:"driver,omitempty"`
	Scope         string            `json:"scope,omitempty"`
	Internal      bool              `json:"internal,omitempty"`
	Attachable    bool              `json:"attachable,omitempty"`
	Created       string            `json:"created,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	SubnetSummary []string          `json:"subnet_summary,omitempty"`
}

type DockerNetworksResult struct {
	Networks []DockerNetworkVM `json:"networks"`
	AuditID  string            `json:"audit_id"`
}

func (a *App) DockerNetworks(targetID string) (*DockerNetworksResult, error) {
	resp, err := a.dockerReadOnly(targetID, "networks", nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Networks []DockerNetworkVM `json:"networks"`
	}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	return &DockerNetworksResult{Networks: raw.Networks, AuditID: resp.AuditID}, nil
}

// dockerReadOnly 是三个只读 list 的公共 helper。
func (a *App) dockerReadOnly(targetID, cmdID string, params any) (*command.Response, error) {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 30*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		raw = b
	}
	return a.engine.Run(ctx, command.Request{
		PluginID:  "docker",
		CommandID: cmdID,
		TargetID:  targetID,
		Params:    raw,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
}

// -----------------------------------------------------------------------------
// DockerExec —— 交互式（xterm）
// -----------------------------------------------------------------------------
//
// 会话模型与 ShellOpen 完全对齐：
//   DockerExecOpen(targetID, in) → sessionID
//   DockerExecWrite(sessionID, base64)
//   DockerExecResize(sessionID, rows, cols)
//   DockerExecClose(sessionID)
//
// 事件：
//   docker:exec:<sid>:stdout    base64(bytes)
//   docker:exec:<sid>:stderr    base64(bytes)
//   docker:exec:<sid>:event     raw json（当前仅 Finish 时可能有）
//   docker:exec:<sid>:exit      { exit_code, error? }

type DockerExecOpenInput struct {
	Container   string   `json:"container"`
	Cmd         []string `json:"cmd"`
	User        string   `json:"user,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	Env         []string `json:"env,omitempty"`
	Tty         bool     `json:"tty,omitempty"`
	AttachStdin bool     `json:"attach_stdin,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	Cols        int      `json:"cols,omitempty"`
}

type dockerExecSession struct {
	id     string
	cancel context.CancelFunc
	stream *desktopDockerExecStream
}

var dockerExecSeq atomic.Int64

// DockerExecOpen 启动一次 exec 会话。
func (a *App) DockerExecOpen(targetID string, in DockerExecOpenInput) (string, error) {
	if in.Container == "" {
		return "", fmt.Errorf("dashboard: container is required")
	}
	if len(in.Cmd) == 0 {
		return "", fmt.Errorf("dashboard: cmd is required")
	}
	sess := fmt.Sprintf("de-%d", dockerExecSeq.Add(1))
	rootCtx := a.wailsCtx()
	ctx, cancel := context.WithCancel(rootCtx)
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		cancel()
		return "", err
	}
	conn, err := a.connMgr.Open(ctx, targetID)
	if err != nil {
		cancel()
		return "", fmt.Errorf("open target %q: %w", targetID, err)
	}

	body := map[string]any{
		"id":            in.Container,
		"cmd":           in.Cmd,
		"tty":           in.Tty,
		"attach_stdin":  in.AttachStdin,
	}
	if in.User != "" {
		body["user"] = in.User
	}
	if in.WorkingDir != "" {
		body["working_dir"] = in.WorkingDir
	}
	if len(in.Env) > 0 {
		body["env"] = in.Env
	}
	params, _ := json.Marshal(body)

	stream := newDesktopDockerExecStream(ctx, rootCtx, sess)
	stream.setParams(params)
	stream.SetConnection(conn)

	a.dockerExecs.Store(sess, &dockerExecSession{id: sess, cancel: cancel, stream: stream})

	go func() {
		defer a.dockerExecs.Delete(sess)
		defer stream.close()
		req := command.Request{
			PluginID:   "docker",
			CommandID:  "exec",
			TargetID:   targetID,
			Connection: conn,
			Params:     params,
			Caller:     sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
		}
		err := a.engine.RunStream(ctx, req, stream)
		exitCode := stream.exitCode()
		payload := map[string]any{"exit_code": exitCode}
		if err != nil {
			payload["error"] = err.Error()
		}
		wailsruntime.EventsEmit(rootCtx, "docker:exec:"+sess+":exit", payload)
	}()

	// 首帧 winch：把前端提供的 rows/cols 提前推给远端，避免第一行输出错位
	if in.Tty && in.Rows > 0 && in.Cols > 0 {
		stream.pushWinch(in.Rows, in.Cols)
	}
	return sess, nil
}

// DockerExecWrite 写入 stdin（base64 编码）。
func (a *App) DockerExecWrite(sessionID, dataB64 string) error {
	v, ok := a.dockerExecs.Load(sessionID)
	if !ok {
		return fmt.Errorf("exec session %q not found", sessionID)
	}
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	v.(*dockerExecSession).stream.pushStdin(raw)
	return nil
}

// DockerExecResize 通知远端 TTY 尺寸变化。
func (a *App) DockerExecResize(sessionID string, rows, cols int) error {
	v, ok := a.dockerExecs.Load(sessionID)
	if !ok {
		return fmt.Errorf("exec session %q not found", sessionID)
	}
	v.(*dockerExecSession).stream.pushWinch(rows, cols)
	return nil
}

// DockerExecClose 主动关闭一次 exec 会话（幂等）。
func (a *App) DockerExecClose(sessionID string) error {
	v, ok := a.dockerExecs.LoadAndDelete(sessionID)
	if !ok {
		return nil
	}
	v.(*dockerExecSession).cancel()
	return nil
}
