package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

type aiChatOpts struct {
	Provider, Model string
	Timeout         time.Duration
	JSON            bool
}

func newAICmd(h *appHolder) *cobra.Command {
	cmd := &cobra.Command{Use: "ai", Short: "Use configured AI providers"}
	cmd.AddCommand(newAIProvidersCmd(h), newAIAskCmd(h))
	return cmd
}

func newAIProvidersCmd(h *appHolder) *cobra.Command {
	return &cobra.Command{Use: "providers", Short: "List configured AI providers", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		resp, err := runAICommand(h, "list_providers", nil, 0)
		if err != nil {
			return err
		}
		var out struct {
			Providers []struct {
				Name         string                   `json:"name"`
				Capabilities sdk.ProviderCapabilities `json:"capabilities"`
			} `json:"providers"`
		}
		if err = json.Unmarshal(resp.Data, &out); err != nil {
			return err
		}
		for _, p := range out.Providers {
			fmt.Fprintf(os.Stdout, "%s\tchat=%t stream=%t tools=%t\t%v\n", p.Name, p.Capabilities.Chat, p.Capabilities.ChatStream, p.Capabilities.ToolCalls, p.Capabilities.Models)
		}
		return nil
	}}
}

func newAIAskCmd(h *appHolder) *cobra.Command {
	o := &aiChatOpts{}
	cmd := &cobra.Command{Use: "ask <prompt>", Short: "Ask an AI provider", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		params, err := json.Marshal(map[string]any{"provider": o.Provider, "model": o.Model, "messages": []sdk.ChatMessage{{Role: sdk.RoleUser, Content: args[0]}}})
		if err != nil {
			return err
		}
		resp, err := runAICommand(h, "chat", params, o.Timeout)
		if err != nil {
			return err
		}
		if o.JSON {
			return json.NewEncoder(os.Stdout).Encode(json.RawMessage(resp.Data))
		}
		var chat sdk.ChatResponse
		if err = json.Unmarshal(resp.Data, &chat); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, chat.Message.Content)
		return nil
	}}
	f := cmd.Flags()
	f.StringVar(&o.Provider, "provider", "", "provider name (default: first configured)")
	f.StringVar(&o.Model, "model", "", "model (default: provider setting)")
	f.DurationVar(&o.Timeout, "timeout", 0, "request timeout")
	f.BoolVar(&o.JSON, "json", false, "print raw response JSON")
	return cmd
}

func runAICommand(h *appHolder, commandID string, params json.RawMessage, timeout time.Duration) (*command.Response, error) {
	app, err := h.Load()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer app.Close(ctx)
	if err = app.ensurePluginEnabled(ctx, "ai"); err != nil {
		return nil, err
	}
	return app.Engine.Run(ctx, command.Request{PluginID: "ai", CommandID: commandID, Params: params, Timeout: timeout, Caller: sdk.Caller{Type: sdk.CallerCLI, User: currentUser()}})
}
