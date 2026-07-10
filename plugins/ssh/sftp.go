package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// ssh.sftp.* —— 基于 pkg/sftp 的一次性文件操作
// -----------------------------------------------------------------------------
//
// 复用 SessionPool 中的 *ssh.Client；每次调用新建一个 *sftp.Client 用完即关。
// 一次性命令即可满足 list / upload / download 三个常见场景；
// 大文件流式传输 / 断点续传等能力放到后续版本再引入 sdk.Stream。

// -----------------------------------------------------------------------------
// 共用工具
// -----------------------------------------------------------------------------

// openSFTP 通过 SessionPool 拿到 *ssh.Client，再建立一个 *sftp.Client。
// 返回值中包含一个 cleanup，调用方必须 defer 之。
func openSFTP(ctx context.Context, pool *SessionPool, plugin *SSHPlugin, conn *sdk.Connection) (*sftp.Client, func(), error) {
	if conn == nil {
		return nil, nil, sdk.ErrConnectionRequired
	}
	dt, err := resolveTarget(conn)
	if err != nil {
		return nil, nil, sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	if dt.Creds.KnownHostsPath == "" && plugin != nil {
		dt.Creds.KnownHostsPath = plugin.defaultKnownHostsPath()
	}

	client, key, err := pool.Acquire(ctx, dt)
	if err != nil {
		return nil, nil, sdk.NewError("SSH_DIAL_FAILED", err.Error(), err).WithRetryable(true)
	}

	sc, err := sftp.NewClient(client)
	if err != nil {
		pool.Evict(key)
		return nil, nil, sdk.NewError("SFTP_OPEN_FAILED", err.Error(), err).WithRetryable(true)
	}
	cleanup := func() {
		_ = sc.Close()
		pool.Release(key)
	}
	return sc, cleanup, nil
}

// -----------------------------------------------------------------------------
// ssh.sftp.list —— 列出远端目录
// -----------------------------------------------------------------------------

type sftpListParams struct {
	// Path 远端目录绝对路径；相对路径会解释为登录用户 $HOME 之下。
	Path string `json:"path"`
}

type sftpEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`     // 例："-rw-r--r--"
	ModTime string `json:"mod_time"` // RFC3339
	IsDir   bool   `json:"is_dir"`
	IsLink  bool   `json:"is_link"`
}

type sftpListResult struct {
	Path    string      `json:"path"`
	Entries []sftpEntry `json:"entries"`
}

type sftpListCmd struct {
	pool   *SessionPool
	plugin *SSHPlugin
}

func (c *sftpListCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "sftp.list",
		Description:    "list a remote directory over SFTP",
		Permission:     sdk.PermRead,
		ConnectionType: "ssh",
	}
}

func (c *sftpListCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func (c *sftpListCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p sftpListParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, sdk.NewError("PARAM_INVALID", "path is required", nil)
	}

	sc, cleanup, err := openSFTP(ctx, c.pool, c.plugin, req.Connection)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 将相对路径（如 "."）解析为绝对路径，避免后续 joinPath 拼出 "./foo" 之
	// 类的奇怪路径。RealPath 失败时回退到原始输入，不阻塞列表操作。
	resolvedPath := p.Path
	if abs, rpErr := sc.RealPath(p.Path); rpErr == nil && abs != "" {
		resolvedPath = abs
	}

	infos, err := sc.ReadDir(p.Path)
	if err != nil {
		return nil, sdk.NewError("SFTP_LIST_FAILED", err.Error(), err)
	}

	entries := make([]sftpEntry, 0, len(infos))
	for _, fi := range infos {
		entries = append(entries, sftpEntry{
			Name:    fi.Name(),
			Size:    fi.Size(),
			Mode:    fi.Mode().String(),
			ModTime: fi.ModTime().UTC().Format(time.RFC3339),
			IsDir:   fi.IsDir(),
			IsLink:  fi.Mode()&os.ModeSymlink != 0,
		})
	}

	data, err := json.Marshal(sftpListResult{Path: resolvedPath, Entries: entries})
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

// -----------------------------------------------------------------------------
// ssh.sftp.upload —— 把本地 / 内联字节写到远端
// -----------------------------------------------------------------------------
//
// 入参二选一：
//   - LocalPath：读取本机文件（插件进程可见的路径）
//   - ContentB64：内联 base64 字节（适合小文件、Recipe / AI 场景）
//
// 若目标目录不存在，且 MkdirAll=true，则递归创建。

type sftpUploadParams struct {
	LocalPath  string `json:"local_path,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
	RemotePath string `json:"remote_path"`

	// Mode 是远端文件的权限位（八进制，例 "0644"）。缺省 0644。
	Mode string `json:"mode,omitempty"`

	// MkdirAll 为 true 时递归创建 remote_path 的父目录。
	MkdirAll bool `json:"mkdir_all,omitempty"`

	// Overwrite 为 true 时允许覆盖已存在文件；默认 true。
	// 显式设为 false 时若目标已存在则报 SFTP_EXISTS。
	Overwrite *bool `json:"overwrite,omitempty"`
}

type sftpUploadResult struct {
	RemotePath string `json:"remote_path"`
	BytesSent  int64  `json:"bytes_sent"`
}

type sftpUploadCmd struct {
	pool   *SessionPool
	plugin *SSHPlugin
}

func (c *sftpUploadCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "sftp.upload",
		Description:    "upload a file to the remote host over SFTP",
		Permission:     sdk.PermWrite,
		ConnectionType: "ssh",
	}
}

func (c *sftpUploadCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func (c *sftpUploadCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p sftpUploadParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.RemotePath == "" {
		return nil, sdk.NewError("PARAM_INVALID", "remote_path is required", nil)
	}
	if p.LocalPath == "" && p.ContentB64 == "" {
		return nil, sdk.NewError("PARAM_INVALID", "either local_path or content_b64 is required", nil)
	}
	if p.LocalPath != "" && p.ContentB64 != "" {
		return nil, sdk.NewError("PARAM_INVALID", "local_path and content_b64 are mutually exclusive", nil)
	}
	mode, err := parseMode(p.Mode, 0o644)
	if err != nil {
		return nil, sdk.NewError("PARAM_INVALID", "invalid mode: "+err.Error(), err)
	}

	// 打开输入源
	var src io.ReadCloser
	if p.LocalPath != "" {
		f, err := os.Open(p.LocalPath)
		if err != nil {
			return nil, sdk.NewError("LOCAL_OPEN_FAILED", err.Error(), err)
		}
		src = f
	} else {
		raw, err := base64.StdEncoding.DecodeString(p.ContentB64)
		if err != nil {
			return nil, sdk.NewError("PARAM_INVALID", "content_b64 decode failed: "+err.Error(), err)
		}
		src = io.NopCloser(bytes.NewReader(raw))
	}
	defer src.Close()

	sc, cleanup, err := openSFTP(ctx, c.pool, c.plugin, req.Connection)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if p.MkdirAll {
		if dir := path.Dir(p.RemotePath); dir != "" && dir != "." && dir != "/" {
			if err := sc.MkdirAll(dir); err != nil {
				return nil, sdk.NewError("SFTP_MKDIR_FAILED", err.Error(), err)
			}
		}
	}

	overwrite := true
	if p.Overwrite != nil {
		overwrite = *p.Overwrite
	}
	if !overwrite {
		if _, statErr := sc.Stat(p.RemotePath); statErr == nil {
			return nil, sdk.NewError("SFTP_EXISTS", "remote_path already exists", nil)
		}
	}

	dst, err := sc.Create(p.RemotePath) // O_WRONLY|O_CREATE|O_TRUNC
	if err != nil {
		return nil, sdk.NewError("SFTP_CREATE_FAILED", err.Error(), err)
	}

	// ctx 取消时关闭 dst 让 io.Copy 出错退出。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = dst.Close()
		case <-done:
		}
	}()

	n, copyErr := io.Copy(dst, src)
	if closeErr := dst.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		if ctx.Err() != nil {
			return nil, sdk.NewError("CANCELED", ctx.Err().Error(), ctx.Err())
		}
		return nil, sdk.NewError("SFTP_WRITE_FAILED", copyErr.Error(), copyErr)
	}

	if err := sc.Chmod(p.RemotePath, mode); err != nil {
		// 不阻塞：某些服务端禁用 chmod。记为可观测事件。
		// 此处保留 err 到 Attributes，不视为失败。
		data, _ := json.Marshal(sftpUploadResult{RemotePath: p.RemotePath, BytesSent: n})
		return &sdk.ExecuteResponse{
			Data:       data,
			Attributes: map[string]string{"chmod_error": err.Error()},
		}, nil
	}

	data, err := json.Marshal(sftpUploadResult{RemotePath: p.RemotePath, BytesSent: n})
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

// -----------------------------------------------------------------------------
// ssh.sftp.download —— 把远端文件读到本地 / 返回内联字节
// -----------------------------------------------------------------------------
//
// 出参二选一：
//   - LocalPath 非空：将远端写入插件进程可见的本机路径
//   - LocalPath 为空：把内容以 base64 返回在 Data.content_b64
//
// 后者仅适合小文件（< 1MiB），大文件请指定 LocalPath 或用 exec+cat 兜底。

const sftpDownloadInlineMaxBytes = 1 << 20 // 1 MiB

type sftpDownloadParams struct {
	RemotePath string `json:"remote_path"`
	LocalPath  string `json:"local_path,omitempty"`

	// Mode 是写入本地文件时使用的权限位（八进制）。缺省 0644。
	Mode string `json:"mode,omitempty"`
}

type sftpDownloadResult struct {
	RemotePath    string `json:"remote_path"`
	LocalPath     string `json:"local_path,omitempty"`
	BytesReceived int64  `json:"bytes_received"`
	// ContentB64 仅在 params.local_path 为空时才填。
	ContentB64 string `json:"content_b64,omitempty"`
}

type sftpDownloadCmd struct {
	pool   *SessionPool
	plugin *SSHPlugin
}

func (c *sftpDownloadCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "sftp.download",
		Description:    "download a file from the remote host over SFTP",
		Permission:     sdk.PermRead,
		ConnectionType: "ssh",
	}
}

func (c *sftpDownloadCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func (c *sftpDownloadCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p sftpDownloadParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.RemotePath == "" {
		return nil, sdk.NewError("PARAM_INVALID", "remote_path is required", nil)
	}
	mode, err := parseMode(p.Mode, 0o644)
	if err != nil {
		return nil, sdk.NewError("PARAM_INVALID", "invalid mode: "+err.Error(), err)
	}

	sc, cleanup, err := openSFTP(ctx, c.pool, c.plugin, req.Connection)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	rf, err := sc.Open(p.RemotePath)
	if err != nil {
		return nil, sdk.NewError("SFTP_OPEN_REMOTE_FAILED", err.Error(), err)
	}
	defer rf.Close()

	// ctx 取消时关闭远端 file
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = rf.Close()
		case <-done:
		}
	}()

	if p.LocalPath != "" {
		lf, err := os.OpenFile(p.LocalPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return nil, sdk.NewError("LOCAL_CREATE_FAILED", err.Error(), err)
		}
		n, copyErr := io.Copy(lf, rf)
		if closeErr := lf.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			if ctx.Err() != nil {
				return nil, sdk.NewError("CANCELED", ctx.Err().Error(), ctx.Err())
			}
			return nil, sdk.NewError("SFTP_READ_FAILED", copyErr.Error(), copyErr)
		}
		data, err := json.Marshal(sftpDownloadResult{
			RemotePath:    p.RemotePath,
			LocalPath:     p.LocalPath,
			BytesReceived: n,
		})
		if err != nil {
			return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
		}
		return &sdk.ExecuteResponse{Data: data}, nil
	}

	// 内联返回：读到内存前先检查大小。
	fi, err := rf.Stat()
	if err != nil {
		return nil, sdk.NewError("SFTP_STAT_FAILED", err.Error(), err)
	}
	if fi.Size() > sftpDownloadInlineMaxBytes {
		return nil, sdk.NewError(
			"SFTP_TOO_LARGE_FOR_INLINE",
			fmt.Sprintf("file size %d exceeds inline limit %d; specify local_path", fi.Size(), sftpDownloadInlineMaxBytes),
			nil,
		)
	}
	buf, err := io.ReadAll(rf)
	if err != nil {
		if ctx.Err() != nil {
			return nil, sdk.NewError("CANCELED", ctx.Err().Error(), ctx.Err())
		}
		return nil, sdk.NewError("SFTP_READ_FAILED", err.Error(), err)
	}
	data, err := json.Marshal(sftpDownloadResult{
		RemotePath:    p.RemotePath,
		BytesReceived: int64(len(buf)),
		ContentB64:    base64.StdEncoding.EncodeToString(buf),
	})
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	return nil
}

// parseMode 解析八进制字符串（例 "0644" / "755"）到 os.FileMode。
// 空串返回 fallback。
func parseMode(s string, fallback os.FileMode) (os.FileMode, error) {
	if s == "" {
		return fallback, nil
	}
	// 允许 "0644" / "644" / "0o644" 三种写法。
	str := s
	if len(str) >= 2 && (str[:2] == "0o" || str[:2] == "0O") {
		str = str[2:]
	}
	var n int64
	for i := 0; i < len(str); i++ {
		ch := str[i]
		if ch < '0' || ch > '7' {
			return 0, errors.New("expect octal digits")
		}
		n = n*8 + int64(ch-'0')
	}
	return os.FileMode(n) & os.ModePerm, nil
}
