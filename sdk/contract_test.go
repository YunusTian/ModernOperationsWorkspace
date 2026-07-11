package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/version"
)

type contractPlugin struct {
	meta     sdk.Metadata
	commands []sdk.CommandHandler
}

func (p *contractPlugin) Metadata() sdk.Metadata                       { return p.meta }
func (p *contractPlugin) Init(context.Context, sdk.InitRequest) error  { return nil }
func (p *contractPlugin) Shutdown(context.Context) error               { return nil }
func (p *contractPlugin) HealthCheck(context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *contractPlugin) Commands() []sdk.CommandHandler               { return p.commands }

type contractCommand struct{ spec sdk.CommandSpec }

func (c *contractCommand) Spec() sdk.CommandSpec { return c.spec }
func (c *contractCommand) Execute(context.Context, *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return &sdk.ExecuteResponse{}, nil
}
func (c *contractCommand) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}

func TestValidatePluginContract(t *testing.T) {
	valid := &contractPlugin{meta: sdk.Metadata{ID: "probe", Name: "Probe", Version: version.Version}, commands: []sdk.CommandHandler{&contractCommand{spec: sdk.CommandSpec{ID: "read", Permission: sdk.PermRead}}}}
	if err := sdk.Validate(valid); err != nil {
		t.Fatalf("valid plugin rejected: %v", err)
	}
	cases := []struct {
		name     string
		p        sdk.Plugin
		contains string
	}{
		{"nil", nil, "plugin is nil"},
		{"missing id", &contractPlugin{meta: sdk.Metadata{Name: "x", Version: "1"}}, "metadata.ID"},
		{"missing name", &contractPlugin{meta: sdk.Metadata{ID: "x", Version: "1"}}, "metadata.Name"},
		{"missing version", &contractPlugin{meta: sdk.Metadata{ID: "x", Name: "x"}}, "metadata.Version"},
		{"nil handler", &contractPlugin{meta: sdk.Metadata{ID: "x", Name: "x", Version: "1"}, commands: []sdk.CommandHandler{nil}}, "nil CommandHandler"},
		{"unspecified permission", &contractPlugin{meta: sdk.Metadata{ID: "x", Name: "x", Version: "1"}, commands: []sdk.CommandHandler{&contractCommand{spec: sdk.CommandSpec{ID: "x"}}}}, "declare permission"},
		{"duplicate", &contractPlugin{meta: sdk.Metadata{ID: "x", Name: "x", Version: "1"}, commands: []sdk.CommandHandler{&contractCommand{spec: sdk.CommandSpec{ID: "x", Permission: sdk.PermRead}}, &contractCommand{spec: sdk.CommandSpec{ID: "x", Permission: sdk.PermRead}}}}, "duplicate command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sdk.Validate(tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error=%v want %q", err, tc.contains)
			}
		})
	}
}

func TestErrorContract(t *testing.T) {
	cause := errors.New("network")
	err := sdk.NewError("PROVIDER_DOWN", "provider unavailable", cause).WithRetryable(true).WithDetails(map[string]any{"status": 503})
	if !errors.Is(err, cause) || !err.Retryable || err.Details["status"] != 503 {
		t.Fatalf("error contract lost: %+v", err)
	}
	if !errors.Is(sdk.NewError("TIMEOUT", "custom", nil), sdk.ErrTimeout) {
		t.Fatal("same stable code must match through errors.Is")
	}
}

func TestAIJSONContract(t *testing.T) {
	in := sdk.ChatResponse{Message: sdk.ChatMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{{ID: "c1", Name: "system.cpu", Args: json.RawMessage(`{"all":true}`)}}}, Usage: sdk.ChatUsage{TotalTokens: 3}, Finish: sdk.FinishToolCalls}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"tool_calls"`, `"finish_reason"`, `"total_tokens"`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("missing JSON field %s in %s", key, b)
		}
	}
	var out sdk.ChatResponse
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Message.ToolCalls[0].Name != "system.cpu" || string(out.Message.ToolCalls[0].Args) != `{"all":true}` {
		t.Fatalf("round trip=%+v", out)
	}
}

func TestStableEnumAndHandshakeContract(t *testing.T) {
	if sdk.PermRead.String() != "read" || sdk.PermDangerous.String() != "dangerous" {
		t.Fatal("permission strings changed")
	}
	if sdk.SignalCancel.String() != "cancel" || sdk.SignalWinch.String() != "winch" {
		t.Fatal("signal strings changed")
	}
	if sdk.StatusHealthy.String() != "healthy" || sdk.StatusUnhealthy.String() != "unhealthy" {
		t.Fatal("health strings changed")
	}
	if sdk.Handshake.ProtocolVersion != 1 || sdk.Handshake.MagicCookieKey != "MOW_PLUGIN" || sdk.PluginSetName != "mow_plugin" {
		t.Fatalf("handshake contract changed: %+v", sdk.Handshake)
	}
	if version.Version == "" {
		t.Fatal("release version is empty")
	}
}
