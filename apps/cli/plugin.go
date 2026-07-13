package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/spf13/cobra"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
)

// -----------------------------------------------------------------------------
// mow plugin ...
//
// v0.5.0 P2：只落地 `mow plugin validate`。
// install / update / enable / disable / uninstall / doctor 在 v0.5.1 完成。
// -----------------------------------------------------------------------------

func newPluginCmd(holder *appHolder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Inspect plugin packages (Manifest, checksum, entrypoint)",
	}
	cmd.AddCommand(
		newPluginValidateCmd(),
		newPluginListCmd(holder),
		newPluginInstallCmd(holder),
		newPluginUpdateCmd(holder),
		newPluginUninstallCmd(holder),
		newPluginToggleCmd(holder, true),
		newPluginToggleCmd(holder, false),
		newPluginDoctorCmd(holder),
		newPluginSearchCmd(holder),
		newPluginCatalogCmd(holder),
	)
	return cmd
}

func pluginLifecycle(holder *appHolder) (*coreplugin.Lifecycle, error) {
	app, err := holder.Load()
	if err != nil {
		return nil, err
	}
	return coreplugin.NewLifecycle(app.Cfg.App.PluginsDir)
}

func newPluginListCmd(holder *appHolder) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed plugin packages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lifecycle, err := pluginLifecycle(holder)
			if err != nil {
				return err
			}
			items, err := lifecycle.List()
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No plugins installed.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ID\tVERSION\tSTATE")
			for _, item := range items {
				state := "disabled"
				if item.Enabled {
					state = "enabled"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", item.ID, item.Version, state)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

func newPluginInstallCmd(holder *appHolder) *cobra.Command {
	var fromPath bool
	var fromCatalog bool
	cmd := &cobra.Command{
		Use:   "install <package|id[@version]>",
		Short: "Install a plugin from a local package or the configured catalog",
		Long: `Install accepts either:
  * a filesystem path to a plugin package directory (or plugin.json), or
  * a plugin identifier like "ssh" / "ssh@0.5.1" that is resolved against
    the configured catalog(s).

By default the argument shape decides the mode: values containing '/' '\'
or beginning with '.' are treated as paths; the rest are treated as
catalog references. Use --path or --catalog to force one of the two modes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginInstallOrUpdate(cmd, holder, args[0], fromPath, fromCatalog, false)
		},
	}
	cmd.Flags().BoolVar(&fromPath, "path", false, "force treating the argument as a local package path")
	cmd.Flags().BoolVar(&fromCatalog, "catalog", false, "force treating the argument as a catalog reference id[@version]")
	return cmd
}

func newPluginUpdateCmd(holder *appHolder) *cobra.Command {
	var fromPath bool
	var fromCatalog bool
	cmd := &cobra.Command{
		Use:   "update <package|id[@version]>",
		Short: "Update an installed plugin from a local package or the catalog",
		Long: `Update swaps an installed plugin with the version found at the
argument. Either a local package path or a catalog reference is
accepted (see 'mow plugin install --help' for details). The existing
installation is atomically replaced and rolled back on failure.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginInstallOrUpdate(cmd, holder, args[0], fromPath, fromCatalog, true)
		},
	}
	cmd.Flags().BoolVar(&fromPath, "path", false, "force treating the argument as a local package path")
	cmd.Flags().BoolVar(&fromCatalog, "catalog", false, "force treating the argument as a catalog reference id[@version]")
	return cmd
}

// runPluginInstallOrUpdate 是 install / update 的共用入口；update=true 时走
// Lifecycle.Update（原子替换 + 回退），否则走 Lifecycle.Install（要求未安装）。
func runPluginInstallOrUpdate(cmd *cobra.Command, holder *appHolder, arg string, forcePath, forceCatalog, update bool) error {
	if forcePath && forceCatalog {
		return fmt.Errorf("--path and --catalog are mutually exclusive")
	}
	viaCatalog := forceCatalog || (!forcePath && coreplugin.LooksLikeCatalogRef(arg))

	lifecycle, err := pluginLifecycle(holder)
	if err != nil {
		return err
	}

	past := "Installed"
	if update {
		past = "Updated"
	}

	if !viaCatalog {
		var item coreplugin.Installation
		var err error
		if update {
			item, err = lifecycle.Update(arg)
		} else {
			item, err = lifecycle.Install(arg)
		}
		if err != nil {
			return err
		}
		state := "disabled"
		if item.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s@%s (%s).\n", past, item.ID, item.Version, state)
		return nil
	}

	// Catalog 路径。
	inst, err := buildInstaller(holder, lifecycle)
	if err != nil {
		return err
	}
	var item coreplugin.Installation
	if update {
		item, err = inst.Update(cmd.Context(), arg)
	} else {
		item, err = inst.Install(cmd.Context(), arg)
	}
	if err != nil {
		return err
	}
	state := "disabled"
	if item.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s@%s (%s) from catalog.\n", past, item.ID, item.Version, state)
	return nil
}

func newPluginUninstallCmd(holder *appHolder) *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall <id>",
		Short: "Uninstall a plugin, preserving state unless --purge is given",
		Long: `Uninstall removes the plugin's package directory. By default the
persisted state (enabled/disabled flag under .state/<id>.json) is kept so
that reinstalling the plugin restores its previous enable state. Pass
--purge to also delete the state file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycle, err := pluginLifecycle(holder)
			if err != nil {
				return err
			}
			if err := lifecycle.Uninstall(args[0], purge); err != nil {
				return err
			}
			if purge {
				fmt.Fprintf(cmd.OutOrStdout(), "Uninstalled %s (state purged).\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Uninstalled %s (state preserved).\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete persisted plugin state (.state/<id>.json)")
	return cmd
}

func newPluginToggleCmd(holder *appHolder, enabled bool) *cobra.Command {
	name, past, short := "enable", "Enabled", "Enable an installed plugin"
	if !enabled {
		name, past, short = "disable", "Disabled", "Disable an installed plugin"
	}
	return &cobra.Command{
		Use:   name + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycle, err := pluginLifecycle(holder)
			if err != nil {
				return err
			}
			item, err := lifecycle.SetEnabled(args[0], enabled)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s@%s.\n", past, item.ID, item.Version)
			return nil
		},
	}
}

func newPluginDoctorCmd(holder *appHolder) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate all installed plugin packages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lifecycle, err := pluginLifecycle(holder)
			if err != nil {
				return err
			}
			items, err := lifecycle.Doctor()
			if err != nil {
				return err
			}
			failed := 0
			for _, item := range items {
				if !item.OK {
					failed++
				}
			}
			if jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(items); err != nil {
					return err
				}
				if failed > 0 {
					return fmt.Errorf("%d installed plugin package(s) failed validation", failed)
				}
				return nil
			}
			for _, item := range items {
				if item.OK {
					fmt.Fprintf(cmd.OutOrStdout(), "ok   %s@%s\n", item.ID, item.Version)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "fail %s@%s: %s\n", item.ID, item.Version, item.Error)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d installed plugin package(s) failed validation", failed)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "OK: %d plugin package(s) healthy.\n", len(items))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

type pluginValidateOpts struct {
	JSON    bool
	Verbose bool
}

func newPluginValidateCmd() *cobra.Command {
	o := &pluginValidateOpts{}
	cmd := &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a plugin package (plugin.json + entrypoint + checksums)",
		Long: `Validate parses plugin.json against the v0.5.0 Manifest schema and
performs filesystem-level checks against the package layout:

  1. plugin.json is well-formed and passes semantic validation
  2. Each platforms[].entrypoint exists inside the package
  3. Each entrypoint's SHA-256 matches the declared checksum
  4. Each recipes[].path / workflows[].path exists

<path> may point at a package directory or directly at plugin.json.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginValidate(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], o)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.JSON, "json", false, "emit machine-readable JSON report")
	f.BoolVarP(&o.Verbose, "verbose", "v", false, "print each check (default: only failures + summary)")
	return cmd
}

// pluginValidateReport 是 --json 输出的稳定 schema。
type pluginValidateReport struct {
	OK         bool                  `json:"ok"`
	Path       string                `json:"path"`
	PackageDir string                `json:"packageDir,omitempty"`
	Manifest   *pluginValidateMeta   `json:"manifest,omitempty"`
	Checks     []pluginValidateCheck `json:"checks"`
	Error      *pluginValidateError  `json:"error,omitempty"`
}

type pluginValidateMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type pluginValidateCheck struct {
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type pluginValidateError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// runPluginValidate 组合 sdk.manifest.Load + ValidatePackage 并渲染报告。
// 返回值：任一失败均返回 error（供 shell exit code 与自动化脚本使用）。
func runPluginValidate(stdout, stderr io.Writer, path string, o *pluginValidateOpts) error {
	report := &pluginValidateReport{Path: path}

	// 1) 先做 Load —— 若 Manifest 本身不合法，直接失败并返回，不再触碰磁盘。
	m, err := manifest.Load(path)
	if err != nil {
		fillError(report, err)
		render(stdout, stderr, report, o)
		return err
	}
	report.Manifest = &pluginValidateMeta{ID: m.ID, Name: m.Name, Version: m.Version}
	report.Checks = append(report.Checks, pluginValidateCheck{Kind: "manifest", OK: true})

	// 2) 磁盘级校验
	pkg, pkgErr := manifest.ValidatePackage(path)
	if pkg != nil {
		report.PackageDir = pkg.PackageDir
		for _, c := range pkg.Checks {
			item := pluginValidateCheck{Kind: c.Kind, Path: c.Path, OK: c.OK}
			if !c.OK && c.Err != nil {
				var se *sdk.Error
				if errors.As(c.Err, &se) {
					item.Code = se.Code
					item.Message = se.Message
				} else {
					item.Message = c.Err.Error()
				}
			}
			report.Checks = append(report.Checks, item)
		}
	}
	if pkgErr != nil {
		fillError(report, pkgErr)
		render(stdout, stderr, report, o)
		return pkgErr
	}

	report.OK = true
	render(stdout, stderr, report, o)
	return nil
}

func fillError(report *pluginValidateReport, err error) {
	report.OK = false
	var se *sdk.Error
	if errors.As(err, &se) {
		report.Error = &pluginValidateError{Code: se.Code, Message: se.Message, Details: se.Details}
	} else {
		report.Error = &pluginValidateError{Code: "UNKNOWN", Message: err.Error()}
	}
}

// -----------------------------------------------------------------------------
// 渲染
// -----------------------------------------------------------------------------

func render(stdout, stderr io.Writer, report *pluginValidateReport, o *pluginValidateOpts) {
	if o.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	renderText(stdout, stderr, report, o)
}

func renderText(stdout, stderr io.Writer, report *pluginValidateReport, o *pluginValidateOpts) {
	if report.Manifest != nil {
		fmt.Fprintf(stdout, "package: %s\n", report.PackageDir)
		fmt.Fprintf(stdout, "plugin:  %s@%s (%s)\n", report.Manifest.ID, report.Manifest.Version, report.Manifest.Name)
	} else {
		fmt.Fprintf(stdout, "path:    %s\n", report.Path)
	}

	pass, fail := 0, 0
	for _, c := range report.Checks {
		if c.OK {
			pass++
			if o.Verbose {
				fmt.Fprintf(stdout, "  ok   %-11s %s\n", c.Kind, c.Path)
			}
		} else {
			fail++
			msg := c.Message
			if c.Code != "" {
				msg = fmt.Sprintf("[%s] %s", c.Code, msg)
			}
			fmt.Fprintf(stderr, "  fail %-11s %s -- %s\n", c.Kind, c.Path, msg)
		}
	}

	if report.OK {
		fmt.Fprintf(stdout, "\nOK: %d checks passed.\n", pass)
		return
	}

	if report.Error != nil {
		fmt.Fprintf(stderr, "\nFAIL: %d passed, %d failed\n", pass, fail)
		fmt.Fprintf(stderr, "error: [%s] %s\n", report.Error.Code, report.Error.Message)
		if len(report.Error.Details) > 0 {
			for _, k := range detailKeys(report.Error.Details) {
				fmt.Fprintf(stderr, "  %s: %v\n", k, report.Error.Details[k])
			}
		}
	}
}

// detailKeys 返回稳定顺序的 details key（field / reason 优先，之后按字典序）。
func detailKeys(d map[string]any) []string {
	priority := []string{"field", "reason", "layer", "actual", "constraint", "path", "expected"}
	seen := map[string]struct{}{}
	out := []string{}
	for _, k := range priority {
		if _, ok := d[k]; ok {
			out = append(out, k)
			seen[k] = struct{}{}
		}
	}
	// 追加其余键（未排序 —— 但 priority 已覆盖高频字段）
	for k := range d {
		if _, ok := seen[k]; ok {
			continue
		}
		out = append(out, k)
	}
	return out
}
