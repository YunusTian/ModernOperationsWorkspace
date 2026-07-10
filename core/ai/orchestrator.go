// Package ai implements host-side AI orchestration. Provider protocol handling
// remains in plugins/ai; all tool execution returns through command.Engine.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

type CommandRunner interface {
	Run(context.Context, command.Request) (*command.Response, error)
	Spec(pluginID, commandID string) (sdk.CommandSpec, error)
}

type Options struct {
	Runner           CommandRunner
	AllowedTools     []string
	MaxRounds        int
	MaxCallsPerRound int
	MaxResultBytes   int
	Timeout          time.Duration
}

type Orchestrator struct {
	runner                         CommandRunner
	tools                          []sdk.ToolSpec
	maxRounds, maxCalls, maxResult int
	timeout                        time.Duration
}

func New(opts Options) (*Orchestrator, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("ai: Runner is required")
	}
	o := &Orchestrator{runner: opts.Runner, maxRounds: opts.MaxRounds, maxCalls: opts.MaxCallsPerRound, maxResult: opts.MaxResultBytes, timeout: opts.Timeout}
	if o.maxRounds <= 0 {
		o.maxRounds = 8
	}
	if o.maxCalls <= 0 {
		o.maxCalls = 4
	}
	if o.maxResult <= 0 {
		o.maxResult = 64 << 10
	}
	if o.timeout <= 0 {
		o.timeout = 120 * time.Second
	}
	for _, fqid := range opts.AllowedTools {
		pluginID, commandID, err := splitFQID(fqid)
		if err != nil {
			return nil, err
		}
		if pluginID == "ai" {
			return nil, fmt.Errorf("ai: recursive tool %q is forbidden", fqid)
		}
		spec, err := opts.Runner.Spec(pluginID, commandID)
		if err != nil {
			return nil, fmt.Errorf("ai: resolve tool %q: %w", fqid, err)
		}
		if spec.Permission != sdk.PermRead {
			return nil, fmt.Errorf("ai: tool %q must have read permission", fqid)
		}
		o.tools = append(o.tools, sdk.ToolSpec{Name: fqid, Description: spec.Description, InputSchema: spec.InputSchema})
	}
	return o, nil
}

type Request struct {
	Provider, Model string
	Messages        []sdk.ChatMessage
	SessionID       string
}
type Result struct {
	Response  sdk.ChatResponse
	Rounds    int
	ToolCalls int
}

func (o *Orchestrator) Run(ctx context.Context, in Request) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	messages := append([]sdk.ChatMessage(nil), in.Messages...)
	totalCalls := 0
	allowed := map[string]bool{}
	for _, t := range o.tools {
		allowed[t.Name] = true
	}
	for round := 1; round <= o.maxRounds; round++ {
		params, _ := json.Marshal(map[string]any{"provider": in.Provider, "model": in.Model, "messages": messages, "tools": o.tools})
		resp, err := o.runner.Run(ctx, command.Request{PluginID: "ai", CommandID: "chat", Params: params, Caller: sdk.Caller{Type: sdk.CallerAI, SessionID: in.SessionID}})
		if err != nil {
			return nil, err
		}
		var chat sdk.ChatResponse
		if err = json.Unmarshal(resp.Data, &chat); err != nil {
			return nil, fmt.Errorf("ai: decode provider response: %w", err)
		}
		if chat.Finish != sdk.FinishToolCalls || len(chat.Message.ToolCalls) == 0 {
			return &Result{Response: chat, Rounds: round, ToolCalls: totalCalls}, nil
		}
		if len(chat.Message.ToolCalls) > o.maxCalls {
			return nil, fmt.Errorf("ai: tool call limit exceeded")
		}
		messages = append(messages, chat.Message)
		for _, tc := range chat.Message.ToolCalls {
			if !allowed[tc.Name] {
				return nil, fmt.Errorf("ai: tool %q is not allowed", tc.Name)
			}
			pluginID, commandID, _ := splitFQID(tc.Name)
			toolResp, runErr := o.runner.Run(ctx, command.Request{PluginID: pluginID, CommandID: commandID, Params: tc.Args, Caller: sdk.Caller{Type: sdk.CallerAI, SessionID: in.SessionID, ParentAuditID: resp.AuditID}})
			var content []byte
			if runErr != nil {
				content, _ = json.Marshal(map[string]any{"error": runErr.Error()})
			} else {
				content = toolResp.Data
			}
			if len(content) > o.maxResult {
				content = append(content[:o.maxResult], []byte(`...`)...)
			}
			messages = append(messages, sdk.ChatMessage{Role: sdk.RoleTool, ToolCallID: tc.ID, Content: string(content)})
			totalCalls++
		}
	}
	return nil, fmt.Errorf("ai: maximum orchestration rounds exceeded")
}

func splitFQID(v string) (string, string, error) {
	p, c, ok := strings.Cut(v, ".")
	if !ok || p == "" || c == "" {
		return "", "", fmt.Errorf("ai: invalid command id %q", v)
	}
	return p, c, nil
}
