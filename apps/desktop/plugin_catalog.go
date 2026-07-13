// plugin_catalog.go —— 桌面客户端 Catalog / Installer Wails 绑定（v0.5.1 P1）。
//
// 复用 core/plugin.Installer 与 core/plugin/catalog.Client：UI 只做数据展现，
// 所有下载 / 校验 / 原子替换 / 回退语义都归 core。
package main

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/plugin/catalog"
	sdkversion "github.com/mow/mow/sdk/version"
)

// -----------------------------------------------------------------------------
// 内部懒加载 catalog.Client
// -----------------------------------------------------------------------------

// catalogState 持有 Client 与懒加载错误；App 里通过 catalogOnce 保护。
type catalogState struct {
	once   sync.Once
	client *catalog.Client
	err    error
}

// catalogClient 装配一次 catalog.Client。任何一次装配失败会缓存在 state 里，
// 后续调用直接返回错误（UI 提示"检查配置"，不会反复重建）。
func (a *App) catalogClient() (*catalog.Client, error) {
	a.catalogStateMu.Lock()
	if a.catalogSt == nil {
		a.catalogSt = &catalogState{}
	}
	st := a.catalogSt
	a.catalogStateMu.Unlock()
	st.once.Do(func() {
		sources := make([]catalog.Source, 0, len(a.cfg.App.Catalog.Sources))
		for _, s := range a.cfg.App.Catalog.Sources {
			sources = append(sources, catalog.Source{Name: s.Name, URL: s.URL, Trusted: s.Trusted})
		}
		cacheDir := a.cfg.App.Catalog.CacheDir
		if cacheDir == "" && a.cfg.App.DataDir != "" {
			cacheDir = filepath.Join(a.cfg.App.DataDir, "catalog-cache")
		}
		st.client, st.err = catalog.NewClient(catalog.Options{
			Sources:  sources,
			CacheDir: cacheDir,
		})
	})
	return st.client, st.err
}

func (a *App) currentFilter() catalog.Filter {
	return catalog.Filter{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CoreVersion: sdkversion.Version,
	}
}

// -----------------------------------------------------------------------------
// 视图模型
// -----------------------------------------------------------------------------

// CatalogSourceVM 是暴露给前端的单个 catalog 源视图（含缓存路径 & 错误）。
type CatalogSourceVM struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Trusted   bool   `json:"trusted"`
	CachePath string `json:"cache_path,omitempty"`
}

// CatalogRefreshResultVM 表示一次拉取结果。
type CatalogRefreshResultVM struct {
	Source     string `json:"source"`
	URL        string `json:"url"`
	OK         bool   `json:"ok"`
	FromCache  bool   `json:"from_cache"`
	NumEntries int    `json:"num_entries,omitempty"`
	FetchedAt  string `json:"fetched_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// CatalogSearchResultVM 是 SearchCatalog 每个源的返回。
type CatalogSearchResultVM struct {
	Source    string          `json:"source"`
	URL       string          `json:"url"`
	FromCache bool            `json:"from_cache"`
	Error     string          `json:"error,omitempty"`
	Entries   []catalog.Entry `json:"entries,omitempty"`
}

// CatalogInstallInput 是 InstallPluginFromCatalog / UpdatePluginFromCatalog 的入参。
type CatalogInstallInput struct {
	// ID 是插件 id；ID 与 Version 拼成 core.Installer 所需的 ref。
	ID string `json:"id"`
	// Version 空 → 由过滤器选出最高兼容版本。
	Version string `json:"version,omitempty"`
}

// -----------------------------------------------------------------------------
// Wails 方法
// -----------------------------------------------------------------------------

// ListCatalogSources 返回全部已配置的 catalog 源（含缓存位置）。
func (a *App) ListCatalogSources() ([]CatalogSourceVM, error) {
	client, err := a.catalogClient()
	if err != nil {
		return nil, err
	}
	sources := client.Sources()
	out := make([]CatalogSourceVM, 0, len(sources))
	for _, s := range sources {
		out = append(out, CatalogSourceVM{
			Name:      s.Name,
			URL:       s.URL,
			Trusted:   s.Trusted,
			CachePath: client.CachePath(s),
		})
	}
	return out, nil
}

// RefreshCatalog 逐个源拉取最新 catalog（force=true → 关闭缓存回退）。
func (a *App) RefreshCatalog(force bool) ([]CatalogRefreshResultVM, error) {
	client, err := a.catalogClient()
	if err != nil {
		return nil, err
	}
	if len(client.Sources()) == 0 {
		return nil, fmt.Errorf("no catalog sources configured; add one under app.catalog.sources")
	}
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 30*time.Second)
	defer cancel()
	results := client.FetchAll(ctx, force)
	out := make([]CatalogRefreshResultVM, 0, len(results))
	for _, r := range results {
		row := CatalogRefreshResultVM{
			Source:    r.Source.Name,
			URL:       r.Source.URL,
			FromCache: r.FromCache,
			FetchedAt: r.FetchedAt.UTC().Format(time.RFC3339),
		}
		if r.Err != nil {
			row.Error = r.Err.Error()
		} else {
			row.OK = true
			if r.Catalog != nil {
				row.NumEntries = len(r.Catalog.Entries)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// SearchCatalog 组合全部源查询；按当前平台 / core 版本过滤，query 支持模糊搜索。
func (a *App) SearchCatalog(query string) ([]CatalogSearchResultVM, error) {
	client, err := a.catalogClient()
	if err != nil {
		return nil, err
	}
	if len(client.Sources()) == 0 {
		return nil, fmt.Errorf("no catalog sources configured")
	}
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 30*time.Second)
	defer cancel()
	results := client.FetchAll(ctx, false)
	filter := a.currentFilter()
	filter.Query = query
	out := make([]CatalogSearchResultVM, 0, len(results))
	for _, r := range results {
		row := CatalogSearchResultVM{Source: r.Source.Name, URL: r.Source.URL, FromCache: r.FromCache}
		if r.Err != nil {
			row.Error = r.Err.Error()
		}
		if r.Catalog != nil {
			row.Entries = r.Catalog.Search(filter)
		}
		out = append(out, row)
	}
	return out, nil
}

// InstallPluginFromCatalog 从 catalog 下载并安装；成功后返回最新 PluginVM 视图。
func (a *App) InstallPluginFromCatalog(in CatalogInstallInput) (PluginVM, error) {
	item, err := a.runInstaller(in, false)
	if err != nil {
		return PluginVM{}, err
	}
	return a.pluginByID(item.ID)
}

// UpdatePluginFromCatalog 走 Lifecycle.Update：原子替换 + 回退。
func (a *App) UpdatePluginFromCatalog(in CatalogInstallInput) (PluginVM, error) {
	item, err := a.runInstaller(in, true)
	if err != nil {
		return PluginVM{}, err
	}
	return a.pluginByID(item.ID)
}

// runInstaller 是 install / update 的共用路径。
func (a *App) runInstaller(in CatalogInstallInput, update bool) (coreplugin.Installation, error) {
	if in.ID == "" {
		return coreplugin.Installation{}, fmt.Errorf("id is required")
	}
	client, err := a.catalogClient()
	if err != nil {
		return coreplugin.Installation{}, err
	}
	if len(client.Sources()) == 0 {
		return coreplugin.Installation{}, fmt.Errorf("no catalog sources configured")
	}
	lifecycle, err := coreplugin.NewLifecycle(a.cfg.App.PluginsDir)
	if err != nil {
		return coreplugin.Installation{}, err
	}
	inst, err := coreplugin.NewInstaller(coreplugin.InstallerOptions{
		Lifecycle: lifecycle,
		Catalog:   client,
		Filter:    a.currentFilter(),
	})
	if err != nil {
		return coreplugin.Installation{}, err
	}
	ref := in.ID
	if in.Version != "" {
		ref = in.ID + "@" + in.Version
	}
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 5*time.Minute)
	defer cancel()
	if update {
		return inst.Update(ctx, ref)
	}
	return inst.Install(ctx, ref)
}
