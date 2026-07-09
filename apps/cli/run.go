package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// mow run <plugin>.<command> [--target ID] [--param k=v ...] [-- ARG...]
// -----------------------------------------------------------------------------

type runOpts struct {
	Target    string
	Params    []string    // k=v，来自 --param
	ParamsRaw string      // 完整 JSON（--params-json）
	Timeout   time.Duration
	Confirmed bool
	AsJSON    bool
}

func newRunCmd(h *appHolder) *cobra.Command {
	o := &runOpts{}
	cmd := &cobra.Command{
		Use:   "run <plugin>.<command> [-- ARG...]",
		Short: "Run a Command via Command Engine",
		Long: `Run a plugin Command through the Command Engine.

Params can be supplied in three ways (later overrides earlier):
  --param key=value       repeatable, k=v style
  --params-json '{...}'   full JSON body
  -- ARG ARG ...          positional args joined by space, assigned to "cmd"
                          (for ssh.exec convenience)

Examples:
  mow run ssh.ping
  mow run ssh.exec --target srv01 -- "uptime"
  mow run ssh.exec --target srv01 --param cmd="df -h"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			fq := args[0]
			rest := args[1:]
			return runRun(h, o, fq, rest)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Target, "target", "", "Connection Target ID (required if command needs a connection)")
	f.StringSliceVar(&o.Params, "param", nil, "parameter in key=value form (repeatable)")
	f.StringVar(&o.ParamsRaw, "params-json", "", "full JSON body")
	f.DurationVar(&o.Timeout, "timeout", 0, "override command timeout (e.g. 30s)")
	f.BoolVar(&o.Confirmed, "yes", false, "pre-confirm Dangerous commands (skip prompt)")
	f.BoolVar(&o.AsJSON, "json", false, "output raw JSON response")
	return cmd
}

func runRun(h *appHolder, o *runOpts, fq string, rest []string) error {
	pluginID, cmdID, err := splitFQID(fq)
	if err != nil {
		return err
	}
	app, err := h.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer app.Close(ctx)

	if err := app.ensurePluginEnabled(ctx, pluginID); err != nil {
		return err
	}

	params, err := buildParams(o, rest)
	if err != nil {
		return err
	}

	req := command.Request{
		PluginID:  pluginID,
		CommandID: cmdID,
		Params:    params,
		TargetID:  o.Target,
		Caller:    sdk.Caller{Type: sdk.CallerCLI, User: currentUser()},
		Timeout:   o.Timeout,
		Confirmed: o.Confirmed,
	}

	resp, err := app.Engine.Run(ctx, req)
	if err != nil {
		return err
	}
	return printResponse(resp, o.AsJSON)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func splitFQID(fq string) (string, string, error) {
	i := strings.IndexByte(fq, '.')
	if i <= 0 || i == len(fq)-1 {
		return "", "", fmt.Errorf("invalid command %q, expected <plugin>.<command>", fq)
	}
	return fq[:i], fq[i+1:], nil
}

// buildParams 合并 --params-json / --param k=v / 位置参数三种来源。
// 优先级（低 → 高）：位置参数 → --param → --params-json
func buildParams(o *runOpts, rest []string) (json.RawMessage, error) {
	m := map[string]any{}

	if len(rest) > 0 {
		m["cmd"] = strings.Join(rest, " ")
	}
	for _, kv := range o.Params {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("invalid --param %q, expected key=value", kv)
		}
		m[parts[0]] = parts[1]
	}

	// 若给了完整 JSON，用它覆盖同名字段
	if o.ParamsRaw != "" {
		var override map[string]any
		if err := json.Unmarshal([]byte(o.ParamsRaw), &override); err != nil {
			return nil, fmt.Errorf("--params-json: %w", err)
		}
		for k, v := range override {
			m[k] = v
		}
	}

	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

func printResponse(resp *command.Response, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}
	fmt.Fprintf(os.Stdout, "audit_id: %s (%.0fms)\n", resp.AuditID,
		float64(resp.Duration.Microseconds())/1000.0)

	if len(resp.Data) == 0 {
		fmt.Fprintln(os.Stdout, "(no data)")
		return nil
	}

	// 尝试识别 ssh.exec 的结构；否则原样输出压平 JSON。
	var execOut struct {
		Stdout   *string `json:"stdout"`
		Stderr   *string `json:"stderr"`
		ExitCode *int    `json:"exit_code"`
	}
	if err := json.Unmarshal(resp.Data, &execOut); err == nil && execOut.ExitCode != nil {
		if execOut.Stdout != nil && *execOut.Stdout != "" {
			fmt.Fprint(os.Stdout, *execOut.Stdout)
			if !strings.HasSuffix(*execOut.Stdout, "\n") {
				fmt.Fprintln(os.Stdout)
			}
		}
		if execOut.Stderr != nil && *execOut.Stderr != "" {
			fmt.Fprint(os.Stderr, *execOut.Stderr)
		}
		if *execOut.ExitCode != 0 {
			return fmt.Errorf("remote exit code %d", *execOut.ExitCode)
		}
		return nil
	}

	// fallback：漂亮打印 JSON
	var pretty any
	if err := json.Unmarshal(resp.Data, &pretty); err == nil {
		out, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Fprintln(os.Stdout, string(out))
		return nil
	}
	fmt.Fprintln(os.Stdout, string(resp.Data))
	return nil
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return "unknown"
}
