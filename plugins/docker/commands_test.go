package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 假 Docker Engine + rig 构造
// -----------------------------------------------------------------------------

type fakeEngine struct {
	srv *httptest.Server
	mu  sync.Mutex
	// 每个路径的处理器：GET/POST 用同一份，路径完全匹配（不含 query）。
	handlers map[string]http.HandlerFunc
	// 记录请求路径与 query，供断言。
	requests []recordedRequest
}

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
}

func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	fe := &fakeEngine{handlers: map[string]http.HandlerFunc{}}
	fe.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fe.mu.Lock()
		fe.requests = append(fe.requests, recordedRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		})
		h, ok := fe.handlers[r.URL.Path]
		fe.mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"no route"}`, http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(func() { fe.srv.Close() })
	return fe
}

func (f *fakeEngine) route(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

func (f *fakeEngine) snapshotRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// hostURL 把 httptest.Server.URL 转成 dockerCredentials.Host 形态：tcp://host:port
func (f *fakeEngine) hostURL() string {
	u, _ := url.Parse(f.srv.URL)
	return "tcp://" + u.Host
}

// connFor 生成一个 sdk.Connection，credentials 指向假 engine。
func (f *fakeEngine) connFor(t *testing.T) *sdk.Connection {
	t.Helper()
	raw, err := json.Marshal(dockerCredentials{Host: f.hostURL()})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return &sdk.Connection{ID: "dk-test", Type: "docker", Credentials: raw}
}

// -----------------------------------------------------------------------------
// docker.list
// -----------------------------------------------------------------------------

func TestListCmd_Basic(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "true" {
			http.Error(w, `{"message":"expected all=true"}`, http.StatusBadRequest)
			return
		}
		body := []engineContainer{
			{
				ID: "abc123", Names: []string{"/nginx"},
				Image: "nginx:latest", State: "running", Status: "Up 10 minutes",
				Ports: []enginePort{{PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})

	params, _ := json.Marshal(listParams{All: true})
	cmd := &listCmd{}
	resp, err := cmd.Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out listResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Containers) != 1 || out.Containers[0].ID != "abc123" {
		t.Fatalf("unexpected output: %+v", out)
	}
	if out.Containers[0].Ports[0].PublicPort != 8080 {
		t.Fatalf("ports mismatch: %+v", out.Containers[0].Ports)
	}
}

func TestListCmd_LabelFilter(t *testing.T) {
	fe := newFakeEngine(t)
	var gotFilter string
	fe.route("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filters")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	params, _ := json.Marshal(listParams{Labels: map[string]string{"app": "web"}})
	if _, err := (&listCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(gotFilter, `"label"`) || !strings.Contains(gotFilter, `app=web`) {
		t.Fatalf("filter query mismatch: %s", gotFilter)
	}
}

func TestListCmd_EngineError(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	_, err := (&listCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected sdk.Error, got %T", err)
	}
	if se.Code != "DOCKER_ENGINE_ERROR" || !se.Retryable {
		t.Fatalf("unexpected code/retryable: %+v", se)
	}
}

// -----------------------------------------------------------------------------
// docker.inspect
// -----------------------------------------------------------------------------

func TestInspectCmd(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"abc","Name":"/nginx"}`))
	})
	params, _ := json.Marshal(inspectParams{ID: "abc"})
	resp, err := (&inspectCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		t.Fatalf("decode inspect data: %v", err)
	}
	if raw["Id"] != "abc" {
		t.Fatalf("passthrough failed: %+v", raw)
	}
}

func TestInspectCmd_NotFound(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/missing/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: missing"}`))
	})
	params, _ := json.Marshal(inspectParams{ID: "missing"})
	_, err := (&inspectCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_NOT_FOUND" {
		t.Fatalf("expected DOCKER_NOT_FOUND, got %+v", err)
	}
}

func TestInspectCmd_MissingID(t *testing.T) {
	fe := newFakeEngine(t)
	_, err := (&inspectCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("expected PARAM_INVALID, got %+v", err)
	}
}

// -----------------------------------------------------------------------------
// docker.start / stop / restart
// -----------------------------------------------------------------------------

func TestLifecycle_Start_OK(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	params, _ := json.Marshal(lifecycleParams{ID: "abc"})
	resp, err := (&startCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out lifecycleResult
	_ = json.Unmarshal(resp.Data, &out)
	if out.Action != "start" || out.AlreadyInState {
		t.Fatalf("bad result: %+v", out)
	}
}

func TestLifecycle_Start_AlreadyRunning(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})
	params, _ := json.Marshal(lifecycleParams{ID: "abc"})
	resp, err := (&startCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out lifecycleResult
	_ = json.Unmarshal(resp.Data, &out)
	if !out.AlreadyInState {
		t.Fatalf("expected already_in_state=true, got %+v", out)
	}
}

func TestLifecycle_Stop_WithTimeout(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "5" {
			http.Error(w, `{"message":"want t=5"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	params, _ := json.Marshal(lifecycleParams{ID: "abc", TimeoutSec: 5})
	if _, err := (&stopCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestLifecycle_Restart_NotFound(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/restart", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"gone"}`))
	})
	params, _ := json.Marshal(lifecycleParams{ID: "abc"})
	_, err := (&restartCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_NOT_FOUND" {
		t.Fatalf("unexpected: %v", err)
	}
}

// -----------------------------------------------------------------------------
// docker.logs
// -----------------------------------------------------------------------------

// buildMuxFrame 生成一段带 8 字节头的 Docker 复用流帧。
func buildMuxFrame(kind stdType, payload []byte) []byte {
	hdr := make([]byte, dockerHeaderSize+len(payload))
	hdr[0] = byte(kind)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	copy(hdr[dockerHeaderSize:], payload)
	return hdr
}

// fakeStream 是 sdk.Stream 的最小内存实现，只覆盖 logsCmd 用到的方法。
type fakeStream struct {
	ctx        context.Context
	params     json.RawMessage
	connection *sdk.Connection
	incoming   chan sdk.Incoming
	stdoutBuf  bytes.Buffer
	stderrBuf  bytes.Buffer
	finished   bool
	finalData  any
	exitCode   int
}

func newFakeStream(ctx context.Context, conn *sdk.Connection, params any) *fakeStream {
	raw, _ := json.Marshal(params)
	return &fakeStream{
		ctx:        ctx,
		params:     raw,
		connection: conn,
		incoming:   make(chan sdk.Incoming, 4),
	}
}

func (s *fakeStream) Context() context.Context      { return s.ctx }
func (s *fakeStream) AuditID() string               { return "" }
func (s *fakeStream) Caller() sdk.Caller            { return sdk.Caller{} }
func (s *fakeStream) Confirmed() bool               { return true }
func (s *fakeStream) RawParams() json.RawMessage    { return s.params }
func (s *fakeStream) Connection() *sdk.Connection   { return s.connection }
func (s *fakeStream) Recv() <-chan sdk.Incoming     { return s.incoming }
func (s *fakeStream) Event(v any) error             { return nil }
func (s *fakeStream) Params(dst any) error          { return json.Unmarshal(s.params, dst) }
func (s *fakeStream) Stdout(data []byte) error      { s.stdoutBuf.Write(data); return nil }
func (s *fakeStream) Stderr(data []byte) error      { s.stderrBuf.Write(data); return nil }
func (s *fakeStream) Finish(v any, exit int) error {
	s.finished = true
	s.finalData = v
	s.exitCode = exit
	return nil
}

func TestLogsCmd_Mux_HistoricalOnly(t *testing.T) {
	// mux 帧序列：stdout=hello / stderr=warn / stdout=world
	body := bytes.Buffer{}
	body.Write(buildMuxFrame(stdStdout, []byte("hello ")))
	body.Write(buildMuxFrame(stdStderr, []byte("warn ")))
	body.Write(buildMuxFrame(stdStdout, []byte("world")))

	fe := newFakeEngine(t)
	fe.route("/containers/abc/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stdout") != "1" || r.URL.Query().Get("stderr") != "1" {
			http.Error(w, `{"message":"expected stdout+stderr"}`, http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("follow") == "1" {
			http.Error(w, `{"message":"follow not expected"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		_, _ = io.Copy(w, &body)
	})

	stream := newFakeStream(context.Background(), fe.connFor(t), logsParams{ID: "abc", Tail: "all"})
	if err := (&logsCmd{}).ExecuteStream(context.Background(), stream); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream.stdoutBuf.String() != "hello world" {
		t.Fatalf("stdout=%q", stream.stdoutBuf.String())
	}
	if stream.stderrBuf.String() != "warn " {
		t.Fatalf("stderr=%q", stream.stderrBuf.String())
	}
	if !stream.finished {
		t.Fatal("expected Finish called")
	}
}

func TestLogsCmd_Tty_Passthrough(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/logs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("raw tty line 1\nraw tty line 2\n"))
	})
	stream := newFakeStream(context.Background(), fe.connFor(t), logsParams{ID: "abc", Tty: true})
	if err := (&logsCmd{}).ExecuteStream(context.Background(), stream); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stream.stdoutBuf.String(), "raw tty line 2") {
		t.Fatalf("tty passthrough missing: %q", stream.stdoutBuf.String())
	}
	if stream.stderrBuf.Len() != 0 {
		t.Fatalf("stderr should be empty in tty mode: %q", stream.stderrBuf.String())
	}
}

func TestLogsCmd_MissingID(t *testing.T) {
	fe := newFakeEngine(t)
	stream := newFakeStream(context.Background(), fe.connFor(t), logsParams{})
	err := (&logsCmd{}).ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("expected PARAM_INVALID, got %v", err)
	}
}

func TestLogsCmd_FollowCanceledBySignal(t *testing.T) {
	// 假 engine 永远阻塞，直到客户端断开
	block := make(chan struct{})
	fe := newFakeEngine(t)
	fe.route("/containers/abc/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		flusher, _ := w.(http.Flusher)
		// 先塞一帧让客户端确认 wire 已建立
		frame := buildMuxFrame(stdStdout, []byte("streaming...\n"))
		_, _ = w.Write(frame)
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream := newFakeStream(ctx, fe.connFor(t), logsParams{ID: "abc", Follow: true})

	done := make(chan error, 1)
	go func() {
		done <- (&logsCmd{}).ExecuteStream(ctx, stream)
	}()

	// 等 stream 至少收到一帧
	for i := 0; i < 20; i++ {
		if stream.stdoutBuf.Len() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	stream.incoming <- &sdk.Signal{Type: sdk.SignalCancel}

	select {
	case err := <-done:
		if err != nil {
			// CANCELED 是可接受的：ctx 已被 Cancel。EOF 也可接受。
			var se *sdk.Error
			if errors.As(err, &se) && se.Code == "CANCELED" {
				return
			}
			t.Fatalf("unexpected err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not exit after signal")
	}
}
