// Package plugin —— 从 Catalog Artifact 下载并落盘为可安装的插件包目录。
//
// 语义与安全约束：
//   - 只支持 http/https/file 三种 scheme（与 catalog.Client 对齐）
//   - MaxBytes 强制封顶下载体积；超限或长度 mismatch 立即失败
//   - checksum 必须以 "sha256:" 前缀 + 64 位 hex；小写规范化
//   - 支持三种打包形态（按后缀自动判定）：目录形式的 plugin.json（file://）、
//     .tar / .tar.gz / .tgz、.zip；解压后必须能在 root 找到 plugin.json
//   - 所有 tar/zip 条目都会做路径 traversal 校验；符号链接一律拒绝
//
// 用法：Download() 返回一个磁盘目录（包含 plugin.json 与 bin/），可直接交给
// Lifecycle.Install / Lifecycle.Update。
package plugin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadOptions 是 Download 的可选参数。
type DownloadOptions struct {
	// HTTPClient 允许注入自定义 http.Client；nil 时使用带 30s 超时的默认值。
	HTTPClient *http.Client
	// MaxBytes 是单次下载的字节上限；0 → 256 MiB。
	MaxBytes int64
}

// Download 从 rawURL 拉取产物、校验 sha256、解压为一个包目录。
// 返回的目录由调用方负责在使用完毕后 os.RemoveAll。
//
// checksum 必须是 "sha256:<64 hex>"。artifactURL 使用 http/https/file scheme。
func Download(ctx context.Context, rawURL, checksum string, opts DownloadOptions) (string, error) {
	if err := validateChecksumFormat(checksum); err != nil {
		return "", err
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 256 * 1024 * 1024
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("plugin download: parse url: %w", err)
	}

	tmp, err := os.MkdirTemp("", "mow-plugin-download-*")
	if err != nil {
		return "", fmt.Errorf("plugin download: mkdtemp: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()

	// 1) 取出原始字节到 blob
	blobPath := filepath.Join(tmp, "artifact"+guessExt(u))
	if err := fetchTo(ctx, opts.HTTPClient, u, blobPath, opts.MaxBytes); err != nil {
		return "", err
	}
	// 2) 校验 checksum
	if err := verifyChecksum(blobPath, checksum); err != nil {
		return "", err
	}
	// 3) 展开为包目录
	pkgDir := filepath.Join(tmp, "package")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return "", err
	}
	if err := extract(blobPath, pkgDir, opts.MaxBytes); err != nil {
		return "", err
	}
	// 4) 修正 root：允许 archive 顶层包一层目录
	root, err := locatePluginRoot(pkgDir)
	if err != nil {
		return "", err
	}
	cleanup = false
	return root, nil
}

func fetchTo(ctx context.Context, hc *http.Client, u *url.URL, dest string, maxBytes int64) error {
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "file":
		p := u.Path
		if p == "" {
			p = u.Opaque
		}
		// Windows: file:///C:/... 会带前导 /，剥掉才能给 os.Open
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		root, err := os.OpenRoot(filepath.Dir(p))
		if err != nil {
			return fmt.Errorf("plugin download: open root for %s: %w", p, err)
		}
		defer root.Close()
		src, err := root.Open(filepath.Base(p))
		if err != nil {
			return fmt.Errorf("plugin download: open %s: %w", p, err)
		}
		defer src.Close()
		return writeLimited(src, dest, maxBytes)
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return fmt.Errorf("plugin download: http: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("plugin download: http %s: %s", u.String(), resp.Status)
		}
		if resp.ContentLength > maxBytes {
			return fmt.Errorf("plugin download: artifact too large (%d > %d)", resp.ContentLength, maxBytes)
		}
		return writeLimited(resp.Body, dest, maxBytes)
	default:
		return fmt.Errorf("plugin download: unsupported scheme %q", u.Scheme)
	}
}

func writeLimited(src io.Reader, dest string, maxBytes int64) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	// 限制 +1 字节以便检测越界
	n, copyErr := io.Copy(out, io.LimitReader(src, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > maxBytes {
		return fmt.Errorf("plugin download: artifact exceeded max %d bytes", maxBytes)
	}
	return nil
}

func validateChecksumFormat(checksum string) error {
	if !strings.HasPrefix(checksum, "sha256:") {
		return fmt.Errorf("plugin download: checksum must start with %q", "sha256:")
	}
	hex := strings.ToLower(strings.TrimPrefix(checksum, "sha256:"))
	if len(hex) != 64 {
		return fmt.Errorf("plugin download: sha256 hex must be 64 chars, got %d", len(hex))
	}
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("plugin download: invalid sha256 hex character %q", c)
		}
	}
	return nil
}

func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("plugin download: checksum mismatch: expected %s, got %s", expected, got)
	}
	return nil
}

// guessExt 根据 URL 猜测 archive 后缀（仅用于给 blob 起个能被 extract 识别的临时名字）。
func guessExt(u *url.URL) string {
	p := strings.ToLower(u.Path)
	switch {
	case strings.HasSuffix(p, ".tar.gz"), strings.HasSuffix(p, ".tgz"):
		return ".tar.gz"
	case strings.HasSuffix(p, ".tar"):
		return ".tar"
	case strings.HasSuffix(p, ".zip"):
		return ".zip"
	case strings.HasSuffix(p, ".json"):
		return ".json"
	}
	return ""
}

// extract 把 blob 展开到 dest。识别 .tar.gz/.tgz、.tar、.zip，以及"裸 plugin.json"三种。
func extract(blobPath, dest string, maxBytes int64) error {
	lower := strings.ToLower(blobPath)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTar(blobPath, dest, true, maxBytes)
	case strings.HasSuffix(lower, ".tar"):
		return extractTar(blobPath, dest, false, maxBytes)
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(blobPath, dest, maxBytes)
	case strings.HasSuffix(lower, ".json"):
		// 只有 plugin.json 也允许（v0.5.1 file:// 场景的极简形式）
		return copyFile(blobPath, filepath.Join(dest, "plugin.json"))
	}
	// 尝试嗅探：以 "PK\x03\x04" 开头 → zip；否则默认按 tar.gz
	f, err := os.Open(blobPath)
	if err != nil {
		return err
	}
	head := make([]byte, 4)
	_, _ = io.ReadFull(f, head)
	f.Close()
	if string(head) == "PK\x03\x04" {
		return extractZip(blobPath, dest, maxBytes)
	}
	return extractTar(blobPath, dest, true, maxBytes)
}

func extractTar(blobPath, dest string, gzipped bool, maxBytes int64) error {
	f, err := os.Open(blobPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("plugin download: gzip: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	remaining := maxBytes
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("plugin download: tar: %w", err)
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || hdr.Size > remaining {
				return fmt.Errorf("plugin download: extracted content exceeds max %d bytes", maxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
				out.Close()
				return err
			}
			remaining -= hdr.Size
			if hdr.FileInfo().Mode()&0o111 != 0 {
				if err := out.Chmod(0o700); err != nil { // #nosec G302 -- executable plugin entries require owner execute permission
					out.Close()
					return err
				}
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("plugin download: symlink %q is not allowed", hdr.Name)
		default:
			// 忽略其他类型（长文件名扩展头等）
		}
	}
	return nil
}

func extractZip(blobPath, dest string, maxBytes int64) error {
	zr, err := zip.OpenReader(blobPath)
	if err != nil {
		return fmt.Errorf("plugin download: zip: %w", err)
	}
	defer zr.Close()
	remaining := uint64(maxBytes)
	for _, entry := range zr.File {
		target, err := safeJoin(dest, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin download: symlink %q is not allowed", entry.Name)
		}
		if entry.UncompressedSize64 > remaining {
			return fmt.Errorf("plugin download: extracted content exceeds max %d bytes", maxBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			in.Close()
			return err
		}
		if _, err := io.CopyN(out, in, int64(entry.UncompressedSize64)); err != nil {
			in.Close()
			out.Close()
			return err
		}
		remaining -= entry.UncompressedSize64
		if entry.Mode()&0o111 != 0 {
			if err := out.Chmod(0o700); err != nil { // #nosec G302 -- executable plugin entries require owner execute permission
				in.Close()
				out.Close()
				return err
			}
		}
		in.Close()
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

// safeJoin 拒绝解压条目穿越 dest；同时把斜杠标准化。
func safeJoin(dest, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("plugin download: empty entry name")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("plugin download: unsafe entry path %q", name)
	}
	target := filepath.Join(dest, clean)
	rel, err := filepath.Rel(dest, target)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("plugin download: entry %q escapes destination", name)
	}
	return target, nil
}

// locatePluginRoot 找出解压后 plugin.json 所在的目录（root 或 root 的第一层子目录）。
func locatePluginRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err == nil {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var candidate string
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
			candidate = filepath.Join(dir, e.Name())
		}
	}
	if dirCount == 1 {
		if _, err := os.Stat(filepath.Join(candidate, "plugin.json")); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("plugin download: plugin.json not found in archive root")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
