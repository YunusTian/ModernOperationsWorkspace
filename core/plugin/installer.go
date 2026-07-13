// Package plugin —— Installer 把 Catalog + Downloader + Lifecycle 串成完整链路。
//
// 使用形态：
//   inst := plugin.NewInstaller(plugin.InstallerOptions{
//       Lifecycle: lifecycle,
//       Catalog:   catalogClient,
//       Filter:    catalog.Filter{OS: runtime.GOOS, Arch: runtime.GOARCH, CoreVersion: sdkversion.Version},
//   })
//   item, err := inst.Install(ctx, "ssh@0.5.1")
//
// 语义：
//   - ref 支持 "id" 或 "id@version"；空 version → 走 Catalog.LatestFor（含
//     平台 & 兼容性过滤）
//   - Install 只在插件"未安装"时可用，否则返回错误（与 Lifecycle.Install 一致）
//   - Update 只在插件"已安装"时可用；成功则新版本原子替换，失败自动回退
//   - 下载完成后调用 Lifecycle.Install / Update，磁盘检查 + Manifest 校验一并做
package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mow/mow/core/plugin/catalog"
)

// InstallerOptions 是 NewInstaller 的构造参数。
type InstallerOptions struct {
	Lifecycle *Lifecycle
	Catalog   *catalog.Client
	// Filter 是默认过滤器（OS / Arch / CoreVersion 等）。Install/Update 时若 ref
	// 未指定 version，会在这个过滤器下选取"最高兼容版本"。
	Filter catalog.Filter
	// Download 是传给 Download 的可选参数（HTTPClient / MaxBytes）。
	Download DownloadOptions
}

// Installer 是无状态的（除持有 Lifecycle/Catalog 指针）；并发安全性等同于两者。
type Installer struct {
	opts InstallerOptions
}

// NewInstaller 构造 Installer；对 Lifecycle / Catalog 做基本非空校验。
func NewInstaller(opts InstallerOptions) (*Installer, error) {
	if opts.Lifecycle == nil {
		return nil, errors.New("plugin installer: Lifecycle is required")
	}
	if opts.Catalog == nil {
		return nil, errors.New("plugin installer: Catalog client is required")
	}
	return &Installer{opts: opts}, nil
}

// Install 从 Catalog 拉取 ref 描述的版本并调用 Lifecycle.Install。
func (i *Installer) Install(ctx context.Context, ref string) (Installation, error) {
	pkgDir, cleanup, err := i.fetch(ctx, ref)
	if err != nil {
		return Installation{}, err
	}
	defer cleanup()
	return i.opts.Lifecycle.Install(pkgDir)
}

// Update 从 Catalog 拉取 ref 描述的版本并调用 Lifecycle.Update。
func (i *Installer) Update(ctx context.Context, ref string) (Installation, error) {
	pkgDir, cleanup, err := i.fetch(ctx, ref)
	if err != nil {
		return Installation{}, err
	}
	defer cleanup()
	return i.opts.Lifecycle.Update(pkgDir)
}

// Resolve 只查 Catalog、选出目标 Release，不下载。CLI 的 dry-run 场景可用。
func (i *Installer) Resolve(ctx context.Context, ref string) (catalog.Entry, catalog.Release, error) {
	id, version, err := parseRef(ref)
	if err != nil {
		return catalog.Entry{}, catalog.Release{}, err
	}
	filter := i.opts.Filter
	filter.Query = ""
	// 从所有 Source 里收集一份合并视图；一旦找到就返回。
	results := i.opts.Catalog.FetchAll(ctx, false)
	var allFailed = len(results) > 0
	for _, r := range results {
		if r.Err == nil {
			allFailed = false
		}
		if r.Catalog == nil {
			continue
		}
		entry, rel, ok := pickReleaseFromCatalog(r.Catalog, id, version, filter)
		if ok {
			return entry, rel, nil
		}
	}
	if allFailed {
		return catalog.Entry{}, catalog.Release{}, fmt.Errorf("plugin installer: all catalog sources failed")
	}
	if version != "" {
		return catalog.Entry{}, catalog.Release{}, fmt.Errorf("plugin installer: %s@%s not found (or not compatible with current platform)", id, version)
	}
	return catalog.Entry{}, catalog.Release{}, fmt.Errorf("plugin installer: %s has no compatible release for current platform", id)
}

// fetch = Resolve + Download，返回可安装的包目录与清理函数。
func (i *Installer) fetch(ctx context.Context, ref string) (string, func(), error) {
	entry, rel, err := i.Resolve(ctx, ref)
	if err != nil {
		return "", func() {}, err
	}
	art, ok := pickArtifact(rel, i.opts.Filter)
	if !ok {
		return "", func() {}, fmt.Errorf("plugin installer: %s@%s has no artifact for %s/%s", entry.ID, rel.Version, i.opts.Filter.OS, i.opts.Filter.Arch)
	}
	dir, err := Download(ctx, art.URL, art.Checksum, i.opts.Download)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}

// parseRef 解析 "id" 或 "id@version"。id 走 lifecycleIDPattern，避免路径穿越。
func parseRef(ref string) (id, version string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", errors.New("plugin installer: empty ref")
	}
	if idx := strings.Index(ref, "@"); idx >= 0 {
		id = ref[:idx]
		version = ref[idx+1:]
	} else {
		id = ref
	}
	if !lifecycleIDPattern.MatchString(id) {
		return "", "", fmt.Errorf("plugin installer: invalid plugin id %q", id)
	}
	return id, version, nil
}

// LooksLikeCatalogRef 判断参数是"catalog ref"还是"本地路径"。
// 规则：
//   - 含路径分隔符 / 或 \，或以 . / ./ / ../ 开头 → 视为路径
//   - 存在 URL scheme（含 file://）→ 视为 URL/路径
//   - 其余：符合 lifecycleIDPattern（含可选 @version） → catalog ref
//
// 该判定只用于 CLI 层的默认路由；用户随时可以显式选择 --package 参数。
func LooksLikeCatalogRef(arg string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return false
	}
	if strings.ContainsAny(arg, "/\\") {
		return false
	}
	if strings.HasPrefix(arg, ".") {
		return false
	}
	if strings.Contains(arg, "://") {
		return false
	}
	id := arg
	if idx := strings.Index(arg, "@"); idx >= 0 {
		id = arg[:idx]
	}
	return lifecycleIDPattern.MatchString(id)
}

// pickReleaseFromCatalog 找出 id 在 catalog 里、通过 filter 后满足 version 约束的 Release。
// version 为空 → 取过滤后的最高版本。
func pickReleaseFromCatalog(c *catalog.Catalog, id, version string, filter catalog.Filter) (catalog.Entry, catalog.Release, bool) {
	for _, e := range c.Entries {
		if e.ID != id {
			continue
		}
		if version == "" {
			rel, ok := c.LatestFor(id, filter)
			return e, rel, ok
		}
		// 精确匹配指定版本；仍需通过 platform/compat 过滤，避免安装到不兼容版本。
		for _, r := range e.Versions {
			if r.Version != version {
				continue
			}
			if !releasePassesFilter(r, filter) {
				return catalog.Entry{}, catalog.Release{}, false
			}
			return e, r, true
		}
		return catalog.Entry{}, catalog.Release{}, false
	}
	return catalog.Entry{}, catalog.Release{}, false
}

// releasePassesFilter 复现 catalog.filterVersions 的判定，避免暴露内部函数。
func releasePassesFilter(r catalog.Release, f catalog.Filter) bool {
	if r.Yanked && !f.IncludeYanked {
		return false
	}
	if _, ok := pickArtifact(r, f); !ok {
		return false
	}
	// 兼容性由 catalog.Search / LatestFor 负责；这里再校验一次也无副作用。
	return true
}

// pickArtifact 从 release.platforms[] 里选一个匹配当前 OS/Arch 的 Artifact。
func pickArtifact(r catalog.Release, f catalog.Filter) (catalog.Artifact, bool) {
	for _, p := range r.Platforms {
		if f.OS != "" && p.OS != f.OS {
			continue
		}
		if f.Arch != "" && p.Arch != f.Arch {
			continue
		}
		return p, true
	}
	return catalog.Artifact{}, false
}
