package grpcbridge

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

func TestConnectionEnvelopeRoundTrip(t *testing.T) {
	params := json.RawMessage(`{"cmd":"uptime"}`)
	conn := &sdk.Connection{ID: "t1", Type: "ssh", Credentials: json.RawMessage(`{"password":"secret"}`), Metadata: map[string]string{"host": "example"}}
	pb, err := paramsWithConnection(params, conn)
	if err != nil {
		t.Fatal(err)
	}
	clean, got := splitConnectionFromParams(pb)
	if got == nil || got.ID != "t1" || got.Type != "ssh" || got.Metadata["host"] != "example" || string(got.Credentials) != string(conn.Credentials) {
		t.Fatalf("connection=%+v", got)
	}
	var m map[string]any
	if err = json.Unmarshal(clean, &m); err != nil || m["cmd"] != "uptime" {
		t.Fatalf("params=%s err=%v", clean, err)
	}
}

func TestEnumConversionsRoundTrip(t *testing.T) {
	for _, v := range []sdk.Permission{sdk.PermUnspecified, sdk.PermRead, sdk.PermWrite, sdk.PermExecute, sdk.PermDangerous} {
		if got := permFromProto(permToProto(v)); got != v {
			t.Fatalf("permission %v -> %v", v, got)
		}
	}
	for _, v := range []sdk.CallerType{sdk.CallerUnspecified, sdk.CallerCLI, sdk.CallerDesktop, sdk.CallerAPI, sdk.CallerAI, sdk.CallerWorkflow, sdk.CallerRecipe} {
		if got := callerTypeFromProto(callerTypeToProto(v)); got != v {
			t.Fatalf("caller %v -> %v", v, got)
		}
	}
	for _, v := range []sdk.SignalType{sdk.SignalUnspecified, sdk.SignalCancel, sdk.SignalInt, sdk.SignalTerm, sdk.SignalKill, sdk.SignalWinch} {
		if got := signalTypeFromProto(signalTypeToProto(v)); got != v {
			t.Fatalf("signal %v -> %v", v, got)
		}
	}
	for _, v := range []sdk.HealthStatus{sdk.StatusUnknown, sdk.StatusHealthy, sdk.StatusDegraded, sdk.StatusUnhealthy} {
		if got := healthFromProto(healthToProto(v)); got != v {
			t.Fatalf("health %v -> %v", v, got)
		}
	}
	if got := durFromProto(durToProto(3 * time.Second)); got != 3*time.Second {
		t.Fatalf("duration=%v", got)
	}
}

func TestErrorConversionContract(t *testing.T) {
	in := sdk.NewError("RATE_LIMITED", "slow down", errors.New("cause")).WithRetryable(true).WithDetails(map[string]any{"status": 429})
	out := errorFromProto(errorToProto(in))
	if out.Code != in.Code || out.Message != in.Message || !out.Retryable || out.Details["status"] != float64(429) {
		t.Fatalf("error=%+v", out)
	}
	unknown := errorFromProto(errorToProto(errors.New("boom")))
	if unknown.Code != "UNKNOWN" || unknown.Message != "boom" {
		t.Fatalf("unknown=%+v", unknown)
	}
}
