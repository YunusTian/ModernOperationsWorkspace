package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	coreai "github.com/mow/mow/core/ai"
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
	cmd.AddCommand(newAIProvidersCmd(h), newAIAskCmd(h), newAIChatCmd(h))
	return cmd
}

func newAIChatCmd(h *appHolder) *cobra.Command {
	o := &aiChatOpts{}
	cmd := &cobra.Command{Use: "chat [prompt]", Short: "Stream an AI conversation", Args: cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		var first string
		if len(args) == 1 {
			first = args[0]
		}
		return runAIChat(h, o, first)
	}}
	f := cmd.Flags()
	f.StringVar(&o.Provider, "provider", "", "provider name (default: first configured)")
	f.StringVar(&o.Model, "model", "", "model (default: provider setting)")
	f.DurationVar(&o.Timeout, "timeout", 0, "timeout per response")
	return cmd
}

func runAIChat(h *appHolder, o *aiChatOpts, first string) error {
	app, err := h.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	defer app.Close(ctx)
	if err = app.ensurePluginEnabled(ctx, "ai"); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	messages := []sdk.ChatMessage{}
	prompt := first
	for {
		if prompt == "" {
			if isTerminal(os.Stdin) {
				fmt.Fprint(os.Stdout, "> ")
			}
			if !scanner.Scan() {
				return scanner.Err()
			}
			prompt = strings.TrimSpace(scanner.Text())
		}
		if prompt == "" {
			continue
		}
		if prompt == "/exit" || prompt == "/quit" {
			return nil
		}
		messages = append(messages, sdk.ChatMessage{Role: sdk.RoleUser, Content: prompt})
		params, _ := json.Marshal(map[string]any{"provider": o.Provider, "model": o.Model, "messages": messages})
		roundCtx := ctx
		cancelRound := func() {}
		if o.Timeout > 0 {
			roundCtx, cancelRound = context.WithTimeout(ctx, o.Timeout)
		}
		stream := newCLIChatStream(roundCtx, params)
		req := command.Request{PluginID: "ai", CommandID: "chat_stream", Params: params, Timeout: o.Timeout, Caller: sdk.Caller{Type: sdk.CallerCLI, User: currentUser()}}
		err = app.Engine.RunStream(roundCtx, req, stream)
		cancelRound()
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout)
		if stream.final.Message.Role != "" {
			messages = append(messages, stream.final.Message)
		}
		if first != "" && !isTerminal(os.Stdin) {
			return nil
		}
		prompt = ""
	}
}

type cliChatStream struct {
	ctx     context.Context
	params  json.RawMessage
	auditID string
	final   sdk.ChatResponse
	mu      sync.Mutex
	recv    chan sdk.Incoming
}

func newCLIChatStream(ctx context.Context, p json.RawMessage) *cliChatStream {
	return &cliChatStream{ctx: ctx, params: p, recv: make(chan sdk.Incoming)}
}
func (s *cliChatStream) SetAuditID(v string)         { s.auditID = v }
func (s *cliChatStream) Context() context.Context    { return s.ctx }
func (s *cliChatStream) AuditID() string             { return s.auditID }
func (s *cliChatStream) Caller() sdk.Caller          { return sdk.Caller{Type: sdk.CallerCLI} }
func (s *cliChatStream) Confirmed() bool             { return false }
func (s *cliChatStream) Params(v any) error          { return json.Unmarshal(s.params, v) }
func (s *cliChatStream) RawParams() json.RawMessage  { return s.params }
func (s *cliChatStream) Connection() *sdk.Connection { return nil }
func (s *cliChatStream) Recv() <-chan sdk.Incoming   { return s.recv }
func (s *cliChatStream) Stdout(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stdout.Write(b)
	return err
}
func (s *cliChatStream) Stderr(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stderr.Write(b)
	return err
}
func (s *cliChatStream) Event(v any) error {
	b, _ := json.Marshal(v)
	fmt.Fprintf(os.Stderr, "\n[tool] %s\n", b)
	return nil
}
func (s *cliChatStream) Finish(v any, _ int) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.final)
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
		return runAIAsk(h, o, args[0])
	}}
	f := cmd.Flags()
	f.StringVar(&o.Provider, "provider", "", "provider name (default: first configured)")
	f.StringVar(&o.Model, "model", "", "model (default: provider setting)")
	f.DurationVar(&o.Timeout, "timeout", 0, "request timeout")
	f.BoolVar(&o.JSON, "json", false, "print raw response JSON")
	return cmd
}

// runAIAsk 走宿主侧 orchestrator：
//   - Tool 目录由 Cfg.AI.AllowedTools 与 CommandSpec 自动派生
//   - 参数 / 结果按 InputSchema 的 x-mow-sensitive 递归脱敏
//   - 决策事件（LoopStart / RoundStart/End / ToolCall / LoopEnd）通过 SlogAuditor
//     写入结构化日志（audit_id 与 core/command 的 AuditRecord 关联）
//   - Ctrl+C 立刻取消上下文，orchestrator 会在超时或 provider 空闲点终止
func runAIAsk(h *appHolder, o *aiChatOpts, prompt string) error {
	app, err := h.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	defer app.Close(ctx)
	if err = app.ensurePluginEnabled(ctx, "ai"); err != nil {
		return err
	}
	orch, err := app.Orchestrator()
	if err != nil {
		return fmt.Errorf("ai orchestrator: %w", err)
	}
	askCtx := ctx
	cancel := func() {}
	if o.Timeout > 0 {
		askCtx, cancel = context.WithTimeout(ctx, o.Timeout)
	}
	defer cancel()

	res, err := orch.Run(askCtx, coreai.Request{
		Provider:  o.Provider,
		Model:     o.Model,
		Messages:  []sdk.ChatMessage{{Role: sdk.RoleUser, Content: prompt}},
		SessionID: fmt.Sprintf("cli-ask-%d", time.Now().UnixNano()),
	})
	if err != nil {
		return err
	}
	if o.JSON {
		return json.NewEncoder(os.Stdout).Encode(res.Response)
	}
	fmt.Fprintln(os.Stdout, res.Response.Message.Content)
	return nil
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
