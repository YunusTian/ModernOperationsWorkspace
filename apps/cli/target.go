package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/connection"
)

// newTargetCmd 构造 `mow target` 命令族。
func newTargetCmd(h *appHolder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Manage Connection Targets (SSH / ...)",
	}
	cmd.AddCommand(
		newTargetAddCmd(h),
		newTargetListCmd(h),
		newTargetRmCmd(h),
	)
	return cmd
}

// -----------------------------------------------------------------------------
// mow target add
// -----------------------------------------------------------------------------

type addOpts struct {
	ID   string
	Type string
	Name string

	Host string
	Port int
	User string

	AuthMethod     string // password / privatekey / agent
	Password       string
	PasswordFile   string
	PrivateKeyFile string
	Passphrase     string

	KnownHostsMode string
	KnownHostsPath string

	Tags []string
}

func newTargetAddCmd(h *appHolder) *cobra.Command {
	o := &addOpts{}
	cmd := &cobra.Command{
		Use:   "add <id>",
		Short: "Add or update a connection target",
		Long: `Add or update a connection target.

Examples:
  # SSH with password (read from --password-file to avoid shell history):
  mow target add srv01 --type ssh --host 10.0.0.1 --user root --auth password --password-file ~/.mow/pw

  # SSH with private key:
  mow target add srv02 --type ssh --host 10.0.0.2 --user ubuntu \
      --auth privatekey --key-file ~/.ssh/id_ed25519

  # SSH via agent:
  mow target add srv03 --type ssh --host 10.0.0.3 --user root --auth agent`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			o.ID = args[0]
			return runTargetAdd(h, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Type, "type", "ssh", "connection type: ssh")
	f.StringVar(&o.Name, "name", "", "human readable name")
	f.StringVar(&o.Host, "host", "", "remote host (required)")
	f.IntVar(&o.Port, "port", 22, "remote port")
	f.StringVar(&o.User, "user", "", "remote user (required)")

	f.StringVar(&o.AuthMethod, "auth", "password", "auth method: password / privatekey / agent")
	f.StringVar(&o.Password, "password", "", "password (prefer --password-file)")
	f.StringVar(&o.PasswordFile, "password-file", "", "file containing password")
	f.StringVar(&o.PrivateKeyFile, "key-file", "", "path to private key file (PEM)")
	f.StringVar(&o.Passphrase, "passphrase", "", "private key passphrase")

	f.StringVar(&o.KnownHostsMode, "known-hosts-mode", "insecure-ignore",
		"host key check: strict / accept-new / insecure-ignore")
	f.StringVar(&o.KnownHostsPath, "known-hosts", "", "known_hosts file path")

	f.StringSliceVar(&o.Tags, "tag", nil, "tag in key=value form (repeatable)")
	return cmd
}

func runTargetAdd(h *appHolder, o *addOpts) error {
	if o.Type != "ssh" {
		return fmt.Errorf("only type=ssh is supported in v0.1, got %q", o.Type)
	}
	app, err := h.Load()
	if err != nil {
		return err
	}

	tags, err := parseTags(o.Tags)
	if err != nil {
		return err
	}

	tg := connection.Target{
		ID:   o.ID,
		Type: connection.Type(o.Type),
		Name: o.Name,
		Host: o.Host,
		Port: o.Port,
		User: o.User,
		Tags: tags,
	}

	creds, err := buildSSHCredentials(o)
	if err != nil {
		return err
	}

	if err := app.ConnMgr.Upsert(tg, creds); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "target %q saved (%s@%s:%d)\n", o.ID, o.User, o.Host, o.Port)
	return nil
}

func buildSSHCredentials(o *addOpts) (*connection.SSHCredentials, error) {
	c := &connection.SSHCredentials{
		Method:         connection.SSHAuthMethod(o.AuthMethod),
		Passphrase:     o.Passphrase,
		KnownHostsMode: o.KnownHostsMode,
		KnownHostsPath: o.KnownHostsPath,
	}
	switch c.Method {
	case connection.SSHAuthPassword:
		pw, err := readSecret(o.Password, o.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("password: %w", err)
		}
		c.Password = pw

	case connection.SSHAuthPrivateKey:
		if o.PrivateKeyFile == "" {
			return nil, fmt.Errorf("--key-file is required for --auth privatekey")
		}
		data, err := os.ReadFile(o.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read key file: %w", err)
		}
		c.PrivateKey = string(data)

	case connection.SSHAuthAgent:
		// nothing to read

	default:
		return nil, fmt.Errorf("unknown --auth %q", o.AuthMethod)
	}
	return c, nil
}

func readSecret(inline, filePath string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if filePath == "" {
		return "", fmt.Errorf("either --password or --password-file is required")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func parseTags(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range pairs {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			return nil, fmt.Errorf("invalid --tag %q, expected key=value", p)
		}
		out[kv[0]] = kv[1]
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// mow target list
// -----------------------------------------------------------------------------

func newTargetListCmd(h *appHolder) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List connection targets",
		RunE: func(c *cobra.Command, args []string) error {
			app, err := h.Load()
			if err != nil {
				return err
			}
			list := app.ConnMgr.List()
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(list)
			}
			if len(list) == 0 {
				fmt.Fprintln(os.Stdout, "(no targets)")
				return nil
			}
			fmt.Fprintf(os.Stdout, "%-20s %-6s %-25s %-15s %s\n",
				"ID", "TYPE", "HOST:PORT", "USER", "NAME")
			for _, t := range list {
				fmt.Fprintf(os.Stdout, "%-20s %-6s %-25s %-15s %s\n",
					t.ID, string(t.Type),
					fmt.Sprintf("%s:%d", t.Host, t.Port),
					t.User, t.Name,
				)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

// -----------------------------------------------------------------------------
// mow target rm
// -----------------------------------------------------------------------------

func newTargetRmCmd(h *appHolder) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a connection target",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			app, err := h.Load()
			if err != nil {
				return err
			}
			if err := app.ConnMgr.Delete(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "target %q removed\n", args[0])
			return nil
		},
	}
}
