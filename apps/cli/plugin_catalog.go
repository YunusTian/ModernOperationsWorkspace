package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/config"
	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/plugin/catalog"
	sdkversion "github.com/mow/mow/sdk/version"
)

// -----------------------------------------------------------------------------
// mow plugin search / mow plugin catalog refresh
//
// 与 `plugin validate/install/update/uninstall/...` 一样挂在 `plugin` 顶部命令下，
// 但内部由本文件负责装配 catalog.Client。
// -----------------------------------------------------------------------------

func newPluginSearchCmd(holder *appHolder) *cobra.Command {
	var (
		jsonOutput bool
		all        bool
		refresh    bool
		os_        string
		arch       string
	)
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search installed catalogs for plugins (filtered by platform & core version)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			app, err := holder.Load()
			if err != nil {
				return err
			}
			client, err := buildCatalogClient(app.Cfg)
			if err != nil {
				return err
			}
			if len(client.Sources()) == 0 {
				return fmt.Errorf("no catalog sources configured; add one under app.catalog.sources")
			}
			results := client.FetchAll(cmd.Context(), refresh)
			filter := catalog.Filter{
				Query:       query,
				OS:          fallback(os_, runtime.GOOS),
				Arch:        fallback(arch, runtime.GOARCH),
				CoreVersion: sdkversion.Version,
			}
			if all {
				filter.OS, filter.Arch = "", ""
			}
			return renderSearchResults(cmd, results, filter, jsonOutput)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON")
	f.BoolVar(&all, "all", false, "include all platforms/arches (skip OS/arch filter)")
	f.BoolVar(&refresh, "refresh", false, "force refresh (skip cache fallback on failure)")
	f.StringVar(&os_, "os", "", "override OS filter (default: runtime.GOOS)")
	f.StringVar(&arch, "arch", "", "override arch filter (default: runtime.GOARCH)")
	return cmd
}

func newPluginCatalogCmd(holder *appHolder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Inspect or refresh plugin catalogs",
	}
	cmd.AddCommand(newPluginCatalogRefreshCmd(holder), newPluginCatalogListCmd(holder))
	return cmd
}

func newPluginCatalogListCmd(holder *appHolder) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured catalog sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := holder.Load()
			if err != nil {
				return err
			}
			client, err := buildCatalogClient(app.Cfg)
			if err != nil {
				return err
			}
			type row struct {
				Name      string `json:"name"`
				URL       string `json:"url"`
				Trusted   bool   `json:"trusted"`
				CachePath string `json:"cache_path,omitempty"`
			}
			rows := make([]row, 0, len(client.Sources()))
			for _, s := range client.Sources() {
				rows = append(rows, row{Name: s.Name, URL: s.URL, Trusted: s.Trusted, CachePath: client.CachePath(s)})
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No catalog sources configured.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "NAME\tURL\tTRUSTED\tCACHE")
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%v\t%s\n", r.Name, r.URL, r.Trusted, r.CachePath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

func newPluginCatalogRefreshCmd(holder *appHolder) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Refresh all configured catalogs (network required)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := holder.Load()
			if err != nil {
				return err
			}
			client, err := buildCatalogClient(app.Cfg)
			if err != nil {
				return err
			}
			results := client.FetchAll(cmd.Context(), true)
			return renderRefreshResults(cmd, results, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func buildCatalogClient(cfg config.Config) (*catalog.Client, error) {
	sources := make([]catalog.Source, 0, len(cfg.App.Catalog.Sources))
	for _, s := range cfg.App.Catalog.Sources {
		sources = append(sources, catalog.Source{Name: s.Name, URL: s.URL, Trusted: s.Trusted})
	}
	cacheDir := cfg.App.Catalog.CacheDir
	if cacheDir == "" && cfg.App.DataDir != "" {
		cacheDir = filepath.Join(cfg.App.DataDir, "catalog-cache")
	}
	return catalog.NewClient(catalog.Options{
		Sources:  sources,
		CacheDir: cacheDir,
	})
}

// buildInstaller 为 install/update 的 catalog 路径装配 Installer。
// 只在真正需要走 catalog 时调用（避免无 catalog 配置时 install <path> 也报错）。
func buildInstaller(holder *appHolder, lifecycle *coreplugin.Lifecycle) (*coreplugin.Installer, error) {
	app, err := holder.Load()
	if err != nil {
		return nil, err
	}
	client, err := buildCatalogClient(app.Cfg)
	if err != nil {
		return nil, err
	}
	if len(client.Sources()) == 0 {
		return nil, fmt.Errorf("no catalog sources configured; add one under app.catalog.sources")
	}
	return coreplugin.NewInstaller(coreplugin.InstallerOptions{
		Lifecycle: lifecycle,
		Catalog:   client,
		Filter: catalog.Filter{
			OS:          runtime.GOOS,
			Arch:        runtime.GOARCH,
			CoreVersion: sdkversion.Version,
		},
	})
}

func fallback(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type searchResultJSON struct {
	Source    string          `json:"source"`
	URL       string          `json:"url"`
	FromCache bool            `json:"from_cache"`
	Error     string          `json:"error,omitempty"`
	Entries   []catalog.Entry `json:"entries,omitempty"`
}

func renderSearchResults(cmd *cobra.Command, results []catalog.FetchResult, filter catalog.Filter, jsonOutput bool) error {
	agg := make([]searchResultJSON, 0, len(results))
	var hardErr error
	for _, r := range results {
		row := searchResultJSON{Source: r.Source.Name, URL: r.Source.URL, FromCache: r.FromCache}
		if r.Err != nil {
			row.Error = r.Err.Error()
			if hardErr == nil {
				hardErr = r.Err
			}
		}
		if r.Catalog != nil {
			row.Entries = r.Catalog.Search(filter)
		}
		agg = append(agg, row)
	}
	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(agg); err != nil {
			return err
		}
	} else {
		total := 0
		for _, r := range agg {
			if r.Error != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "warn: catalog %q unavailable: %s\n", r.Source, r.Error)
				continue
			}
			cacheNote := ""
			if r.FromCache {
				cacheNote = " (from cache)"
			}
			if len(r.Entries) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] no matches%s\n", r.Source, cacheNote)
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "[%s]%s\n", r.Source, cacheNote)
			for _, e := range r.Entries {
				latest := e.Versions[0]
				fmt.Fprintf(cmd.OutOrStdout(), "  %s@%s\t%s\n", e.ID, latest.Version, e.Name)
			}
			total += len(r.Entries)
		}
		if total == 0 && hardErr == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "No matching plugins found.")
		}
	}
	// 只有当所有源都失败且没有任何缓存回退时才返回错误。
	if allFailed(agg) {
		return fmt.Errorf("all catalog sources failed; try running 'mow plugin catalog refresh' with a working network")
	}
	return nil
}

func allFailed(rows []searchResultJSON) bool {
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if r.Error == "" {
			return false
		}
	}
	return true
}

type refreshResultJSON struct {
	Source     string `json:"source"`
	URL        string `json:"url"`
	OK         bool   `json:"ok"`
	FromCache  bool   `json:"from_cache"`
	NumEntries int    `json:"num_entries,omitempty"`
	Error      string `json:"error,omitempty"`
}

func renderRefreshResults(cmd *cobra.Command, results []catalog.FetchResult, jsonOutput bool) error {
	rows := make([]refreshResultJSON, 0, len(results))
	var firstErr error
	for _, r := range results {
		row := refreshResultJSON{Source: r.Source.Name, URL: r.Source.URL, FromCache: r.FromCache}
		if r.Err != nil {
			row.Error = r.Err.Error()
			if firstErr == nil {
				firstErr = r.Err
			}
		} else {
			row.OK = true
			if r.Catalog != nil {
				row.NumEntries = len(r.Catalog.Entries)
			}
		}
		rows = append(rows, row)
	}
	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return err
		}
	} else {
		for _, r := range rows {
			if r.OK {
				fmt.Fprintf(cmd.OutOrStdout(), "ok   %s\t%d entries\n", r.Source, r.NumEntries)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "fail %s\t%s\n", r.Source, r.Error)
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

// 静态引用避免 goimports 删掉未使用的包（供未来 install 从 catalog 复用）。
