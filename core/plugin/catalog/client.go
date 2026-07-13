package catalog

import (
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

// Source 表示一份 Catalog 的来源（远端或本地）。
type Source struct {
	// Name 是展示名（例如 "official"）。同一 Client 内应唯一。
	Name string
	// URL 支持三种 scheme：http/https（远端）、file（本地）。空 URL 无效。
	URL string
	// Trusted 保留供 v0.5.2 使用；v0.5.1 尚未接入可信策略。
	Trusted bool
}

// Options 是 Client 构造参数。
type Options struct {
	// Sources 是官方 + 自定义源的完整列表；重名会 Load 时报错。
	Sources []Source
	// CacheDir 是缓存根目录（每个 Source 一份子目录）。空表示禁用缓存。
	CacheDir string
	// HTTPClient 允许注入自定义 http.Client；nil 时使用带 10s 超时的默认值。
	HTTPClient *http.Client
	// Now 供测试注入；nil 时使用 time.Now。
	Now func() time.Time
	// MaxBytes 是单个 Catalog 拉取的字节上限（防御性），0 → 8 MiB。
	MaxBytes int64
}

// Client 负责按 Source 拉取 Catalog、写入缓存并支持离线读取。
//
// 语义：
//   - Fetch(force=false)：先尝试远端；失败自动回退缓存
//   - Fetch(force=true)：仅拉远端，失败即返回错误（不回退缓存），
//     但已有的缓存文件不会被删除
//   - LoadCached：只读缓存
//
// 所有方法并发安全。
type Client struct {
	sources    []Source
	byName     map[string]Source
	cacheDir   string
	httpClient *http.Client
	now        func() time.Time
	maxBytes   int64
}

// NewClient 校验并构造 Client。
func NewClient(opts Options) (*Client, error) {
	byName := make(map[string]Source, len(opts.Sources))
	for _, s := range opts.Sources {
		if strings.TrimSpace(s.Name) == "" {
			return nil, errors.New("catalog: source name is empty")
		}
		if strings.TrimSpace(s.URL) == "" {
			return nil, fmt.Errorf("catalog: source %q has empty URL", s.Name)
		}
		if _, dup := byName[s.Name]; dup {
			return nil, fmt.Errorf("catalog: duplicate source name %q", s.Name)
		}
		u, err := url.Parse(s.URL)
		if err != nil {
			return nil, fmt.Errorf("catalog: source %q url: %w", s.Name, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "file":
		default:
			return nil, fmt.Errorf("catalog: source %q unsupported scheme %q", s.Name, u.Scheme)
		}
		byName[s.Name] = s
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	mb := opts.MaxBytes
	if mb <= 0 {
		mb = 8 * 1024 * 1024
	}
	return &Client{
		sources:    append([]Source(nil), opts.Sources...),
		byName:     byName,
		cacheDir:   opts.CacheDir,
		httpClient: hc,
		now:        now,
		maxBytes:   mb,
	}, nil
}

// Sources 返回当前已注册的 Source 列表副本，按注册顺序。
func (c *Client) Sources() []Source {
	return append([]Source(nil), c.sources...)
}

// FetchResult 记录一次 Fetch 的结果。
type FetchResult struct {
	Source     Source
	Catalog    *Catalog
	FromCache  bool // true 表示走了缓存回退
	FetchedAt  time.Time
	Err        error // 网络+缓存都失败时才非 nil；即使 FromCache=true 也可能有 Err（仅当也无缓存）
}

// FetchAll 顺序拉取所有 Source。force=true 时任一 Source 网络失败即返回 error 集合。
// force=false 时优先使用缓存回退，网络错误不阻断其他 Source。
func (c *Client) FetchAll(ctx context.Context, force bool) []FetchResult {
	out := make([]FetchResult, 0, len(c.sources))
	for _, s := range c.sources {
		out = append(out, c.Fetch(ctx, s, force))
	}
	return out
}

// Fetch 按单个 Source 拉取；见 Client 说明。
func (c *Client) Fetch(ctx context.Context, s Source, force bool) FetchResult {
	res := FetchResult{Source: s, FetchedAt: c.now().UTC()}
	data, err := c.fetchRemote(ctx, s)
	if err == nil {
		cat, parseErr := Parse(data)
		if parseErr == nil {
			cat.Source = s.Name
			cat.URL = s.URL
			// 缓存写入失败不影响返回。
			_ = c.writeCache(s, data)
			res.Catalog = cat
			return res
		}
		err = fmt.Errorf("parse remote: %w", parseErr)
	}
	if force {
		res.Err = err
		return res
	}
	// 回退缓存
	if cached, cacheErr := c.LoadCached(s); cacheErr == nil {
		res.Catalog = cached
		res.FromCache = true
		return res
	}
	res.Err = err
	return res
}

// LoadCached 仅从缓存读取。缓存不存在返回 fs.ErrNotExist 系列错误。
func (c *Client) LoadCached(s Source) (*Catalog, error) {
	if c.cacheDir == "" {
		return nil, errors.New("catalog: cache disabled")
	}
	path := c.cachePath(s)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cat, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse cache %s: %w", path, err)
	}
	cat.Source = s.Name
	cat.URL = s.URL
	return cat, nil
}

// CachePath 暴露缓存路径供 CLI 展示。
func (c *Client) CachePath(s Source) string {
	if c.cacheDir == "" {
		return ""
	}
	return c.cachePath(s)
}

func (c *Client) cachePath(s Source) string {
	// 用 sha256(name+URL) 作为缓存文件名，避免任意 name/URL 中的特殊字符干扰路径。
	sum := sha256.Sum256([]byte(s.Name + "\x00" + s.URL))
	return filepath.Join(c.cacheDir, hex.EncodeToString(sum[:])+".json")
}

func (c *Client) writeCache(s Source, data []byte) error {
	if c.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.cacheDir, 0o700); err != nil {
		return err
	}
	path := c.cachePath(s)
	tmp, err := os.CreateTemp(c.cacheDir, ".catalog-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 原子替换。Windows 也支持 Rename 到已存在的文件。
	return os.Rename(tmpName, path)
}

func (c *Client) fetchRemote(ctx context.Context, s Source) ([]byte, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "file":
		p := u.Path
		if p == "" {
			p = u.Opaque
		}
		// Windows: file:///C:/... 会带前导 /，需要剥掉才能给 os.ReadFile
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		return os.ReadFile(p)
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("http %s: %s", s.URL, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, c.maxBytes))
	default:
		return nil, fmt.Errorf("catalog: unsupported scheme %q", u.Scheme)
	}
}
