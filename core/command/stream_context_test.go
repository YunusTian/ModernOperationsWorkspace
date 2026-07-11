package command_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

type connectionStreamHandler struct {
	seen chan *sdk.Connection
}

func (h *connectionStreamHandler) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{ID: "stream", Permission: sdk.PermRead, Streaming: true, ConnectionType: "docker"}
}
func (h *connectionStreamHandler) Execute(context.Context, *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}
func (h *connectionStreamHandler) ExecuteStream(_ context.Context, stream sdk.Stream) error {
	h.seen <- stream.Connection()
	return nil
}

type testStream struct {
	ctx context.Context
}

func (s *testStream) Context() context.Context    { return s.ctx }
func (s *testStream) AuditID() string             { return "transport-audit" }
func (s *testStream) Caller() sdk.Caller          { return sdk.Caller{} }
func (s *testStream) Confirmed() bool             { return false }
func (s *testStream) Params(any) error            { return nil }
func (s *testStream) RawParams() json.RawMessage  { return json.RawMessage(`{}`) }
func (s *testStream) Connection() *sdk.Connection { return nil }
func (s *testStream) Recv() <-chan sdk.Incoming   { return make(chan sdk.Incoming) }
func (s *testStream) Stdout([]byte) error         { return nil }
func (s *testStream) Stderr([]byte) error         { return nil }
func (s *testStream) Event(any) error             { return nil }
func (s *testStream) Finish(any, int) error       { return nil }

func TestRunStreamBindsResolvedConnection(t *testing.T) {
	handler := &connectionStreamHandler{seen: make(chan *sdk.Connection, 1)}
	pm := plugin.NewManager(plugin.Options{})
	if err := pm.Register(&fakePlugin{id: "demo", cmds: []sdk.CommandHandler{handler}}); err != nil {
		t.Fatal(err)
	}
	if err := pm.Enable(context.Background(), "demo", sdk.InitRequest{}); err != nil {
		t.Fatal(err)
	}
	resolver := &stubResolver{conn: &sdk.Connection{ID: "docker-1", Type: "docker"}}
	engine := command.New(command.Options{Manager: pm, Resolver: resolver, Confirm: command.AllowConfirmer{}})

	err := engine.RunStream(context.Background(), command.Request{
		PluginID: "demo", CommandID: "stream", TargetID: "docker-1",
		Params: json.RawMessage(`{"all":true}`), Caller: sdk.Caller{Type: sdk.CallerCLI},
	}, &testStream{ctx: context.Background()})
	if err != nil {
		t.Fatalf("RunStream() error: %v", err)
	}
	if got := <-handler.seen; got == nil || got.ID != "docker-1" {
		t.Fatalf("stream connection = %+v, want docker-1", got)
	}
}
