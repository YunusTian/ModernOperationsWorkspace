package conformance_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/conformance"
)

// --- fixture plugins --------------------------------------------------------

type samplePlugin struct {
	handlers []sdk.CommandHandler
	inits    int
	shuts    int
}

func (p *samplePlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "sample", Name: "Sample", Version: "0.1.0"}
}
func (p *samplePlugin) Init(context.Context, sdk.InitRequest) error {
	p.inits++
	return nil
}
func (p *samplePlugin) Shutdown(context.Context) error {
	p.shuts++
	return nil
}
func (p *samplePlugin) HealthCheck(context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *samplePlugin) Commands() []sdk.CommandHandler               { return p.handlers }

type readCmd struct{}

func (readCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{ID: "ping", Permission: sdk.PermRead}
}
func (readCmd) Execute(_ context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return &sdk.ExecuteResponse{Data: append(json.RawMessage(nil), req.Params...)}, nil
}
func (readCmd) ExecuteStream(context.Context, sdk.Stream) error { return sdk.ErrNotSupported }

type dangerousCmd struct{}

func (dangerousCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{ID: "wipe", Permission: sdk.PermDangerous}
}
func (dangerousCmd) Execute(_ context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if !req.Confirmed {
		return nil, sdk.ErrConfirmationRequired
	}
	return &sdk.ExecuteResponse{}, nil
}
func (dangerousCmd) ExecuteStream(context.Context, sdk.Stream) error { return sdk.ErrNotSupported }

// echoStreamCmd 回显 stdin 到 stdout，并把 Params.text 作为 finalData 提交。
type echoStreamCmd struct{}

func (echoStreamCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{ID: "echo", Permission: sdk.PermExecute, Streaming: true}
}
func (echoStreamCmd) Execute(context.Context, *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}
func (echoStreamCmd) ExecuteStream(_ context.Context, s sdk.Stream) error {
	var p struct {
		Text string `json:"text"`
	}
	if err := s.Params(&p); err != nil {
		return err
	}
	select {
	case msg, ok := <-s.Recv():
		if !ok {
			return errors.New("inbox closed before first frame")
		}
		if in, ok := msg.(*sdk.Stdin); ok {
			_ = s.Stdout(in.Data)
		}
	case <-s.Context().Done():
		return s.Context().Err()
	}
	return s.Finish(map[string]string{"echoed": p.Text}, 0)
}

// --- tests ------------------------------------------------------------------

func TestConformance_Lifecycle(t *testing.T) {
	p := &samplePlugin{handlers: []sdk.CommandHandler{readCmd{}, dangerousCmd{}, echoStreamCmd{}}}

	conformance.Run(t, conformance.Suite{
		Plugin: p,
		Cases: []conformance.Case{
			{
				CommandID: "ping",
				Params:    map[string]any{"hello": "world"},
				Check: func(t *testing.T, r conformance.Result) {
					if r.ExecErr != nil {
						t.Fatalf("exec: %v", r.ExecErr)
					}
					if !strings.Contains(string(r.Response.Data), `"hello":"world"`) {
						t.Fatalf("params not echoed: %s", r.Response.Data)
					}
				},
			},
			{
				Name:      "wipe rejects when unconfirmed",
				CommandID: "wipe",
			},
			{
				Name:      "wipe succeeds with confirmed",
				CommandID: "wipe",
				Confirmed: true,
				Check: func(t *testing.T, r conformance.Result) {
					if r.ExecErr != nil {
						t.Fatalf("wipe confirmed failed: %v", r.ExecErr)
					}
				},
			},
			{
				CommandID:    "echo",
				Params:       map[string]any{"text": "conformance"},
				StreamInputs: []sdk.Incoming{&sdk.Stdin{Data: []byte("hi")}},
				Check: func(t *testing.T, r conformance.Result) {
					if r.StreamErr != nil {
						t.Fatalf("stream: %v", r.StreamErr)
					}
					if !r.StreamDone {
						t.Fatal("stream did not finish")
					}
					chunks := r.Stream.StdoutChunks()
					if len(chunks) != 1 || string(chunks[0]) != "hi" {
						t.Fatalf("stdout=%q", chunks)
					}
					if !strings.Contains(string(r.Stream.FinalData()), `"echoed":"conformance"`) {
						t.Fatalf("final=%s", r.Stream.FinalData())
					}
				},
			},
		},
	})

	if p.inits != 1 || p.shuts != 1 {
		t.Fatalf("lifecycle not driven exactly once: init=%d shutdown=%d", p.inits, p.shuts)
	}
}
