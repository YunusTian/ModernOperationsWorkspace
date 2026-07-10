// stage3_test.go —— v0.3 第三阶段命令的单测。
//
// 覆盖：
//   - docker.rm：Confirmed 校验、force/volumes query、404 错误映射
//   - docker.pull：URL query、progress 事件转发、error 行映射
//   - docker.push：X-Registry-Auth 头、错误分类（unauthorized/not found）
//   - docker.exec：暂只覆盖参数验证与 create 阶段的错误路径（hijack 需要
//     真实 upgrade，httptest 不便模拟；主路径在集成测试覆盖）
//
// 复用 commands_test.go 的 fakeEngine / fakeStream / hostURL / connFor。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 事件收集版 fakeStream
// -----------------------------------------------------------------------------

// eventStream 在 fakeStream 之上记录 Event 事件，方便断言 progress 行。
type eventStream struct {
	*fakeStream
	mu     sync.Mutex
	events []any
}

func newEventStream(ctx context.Context, conn *sdk.Connection, params any) *eventStream {
	return &eventStream{fakeStream: newFakeStream(ctx, conn, params)}
}

func (s *eventStream) Event(v any) error {
	s.mu.Lock()
	s.events = append(s.events, v)
	s.mu.Unlock()
	return nil
}
func (s *eventStream) snapshot() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]any, len(s.events))
	copy(out, s.events)
	return out
}

// -----------------------------------------------------------------------------
// docker.rm
// -----------------------------------------------------------------------------

func TestRmCmd_MissingConfirmationRejected(t *testing.T) {
	fe := newFakeEngine(t)
	params, _ := json.Marshal(rmParams{ID: "abc"})
	_, err := (&rmCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
		Confirmed:  false,
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "CONFIRMATION_REQUIRED" {
		t.Fatalf("expected CONFIRMATION_REQUIRED, got %v", err)
	}
	// 未经确认时不应向 Engine 发任何请求
	if len(fe.snapshotRequests()) != 0 {
		t.Fatalf("Engine should not be called before confirmation: %+v", fe.snapshotRequests())
	}
}

func TestRmCmd_ForceAndVolumesQuery(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "want DELETE", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	params, _ := json.Marshal(rmParams{ID: "abc", Force: true, Volumes: true})
	resp, err := (&rmCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
		Confirmed:  true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out rmResult
	_ = json.Unmarshal(resp.Data, &out)
	if out.ID != "abc" {
		t.Fatalf("result: %+v", out)
	}
	reqs := fe.snapshotRequests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 req, got %+v", reqs)
	}
	if reqs[0].Query.Get("force") != "true" || reqs[0].Query.Get("v") != "true" {
		t.Fatalf("query: %v", reqs[0].Query)
	}
}

func TestRmCmd_NotFoundMapped(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such container"}`))
	})
	params, _ := json.Marshal(rmParams{ID: "nope"})
	_, err := (&rmCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t), Params: params, Confirmed: true,
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_NOT_FOUND" {
		t.Fatalf("want DOCKER_NOT_FOUND, got %v", err)
	}
}

func TestRmCmd_MissingID(t *testing.T) {
	fe := newFakeEngine(t)
	_, err := (&rmCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t), Confirmed: true,
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("want PARAM_INVALID, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// docker.pull
// -----------------------------------------------------------------------------

func TestPullCmd_HappyPath_EmitsProgress(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/images/create", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromImage") != "nginx" || r.URL.Query().Get("tag") != "1.25" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		flusher, _ := w.(http.Flusher)
		lines := []string{
			`{"status":"Pulling from library/nginx","id":"1.25"}`,
			`{"status":"Downloading","progressDetail":{"current":100,"total":1000},"id":"abc"}`,
			`{"status":"Pull complete","id":"abc"}`,
		}
		for _, l := range lines {
			_, _ = w.Write([]byte(l + "\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	stream := newEventStream(context.Background(), fe.connFor(t),
		pullParams{FromImage: "nginx", Tag: "1.25"})
	if err := (&pullCmd{}).ExecuteStream(context.Background(), stream); err != nil {
		t.Fatalf("execute: %v", err)
	}
	ev := stream.snapshot()
	if len(ev) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(ev), ev)
	}
	if !stream.finished {
		t.Fatal("Finish not called")
	}
}

func TestPullCmd_ErrorLineMapped(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/images/create", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errorDetail":{"message":"pull access denied - unauthorized"},"error":"pull access denied - unauthorized"}` + "\n"))
	})
	stream := newEventStream(context.Background(), fe.connFor(t),
		pullParams{FromImage: "secret/app"})
	err := (&pullCmd{}).ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("want sdk.Error, got %v", err)
	}
	if se.Code != "DOCKER_UNAUTHORIZED" {
		t.Fatalf("want DOCKER_UNAUTHORIZED, got %s (%s)", se.Code, se.Message)
	}
	if stream.finished {
		t.Fatal("Finish should not be called on error")
	}
}

func TestPullCmd_MissingImage(t *testing.T) {
	fe := newFakeEngine(t)
	stream := newEventStream(context.Background(), fe.connFor(t), pullParams{})
	err := (&pullCmd{}).ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("want PARAM_INVALID, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// docker.push
// -----------------------------------------------------------------------------

func TestPushCmd_SendsAuthHeader(t *testing.T) {
	fe := newFakeEngine(t)
	var gotAuth string
	fe.route("/images/registry.example.com/team/app/push", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Registry-Auth")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"Preparing"}` + "\n"))
	})
	stream := newEventStream(context.Background(), fe.connFor(t),
		pushParams{
			Image: "registry.example.com/team/app",
			Tag:   "v1",
			Auth:  &registryAuth{Username: "u", Password: "p"},
		})
	if err := (&pushCmd{}).ExecuteStream(context.Background(), stream); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotAuth == "" {
		t.Fatal("X-Registry-Auth was not sent")
	}
	// 解码回来校验
	dec, err := base64.URLEncoding.DecodeString(gotAuth)
	if err != nil {
		t.Fatalf("decode auth: %v", err)
	}
	if !strings.Contains(string(dec), `"username":"u"`) {
		t.Fatalf("auth body missing username: %s", dec)
	}
}

func TestPushCmd_AnonymousStillSendsEmptyAuth(t *testing.T) {
	fe := newFakeEngine(t)
	var gotAuth string
	fe.route("/images/lib/app/push", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Registry-Auth")
		w.Header().Set("Content-Type", "application/json")
	})
	stream := newEventStream(context.Background(), fe.connFor(t),
		pushParams{Image: "lib/app"})
	if err := (&pushCmd{}).ExecuteStream(context.Background(), stream); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotAuth == "" {
		t.Fatal("engine requires header even for anonymous push")
	}
	dec, _ := base64.URLEncoding.DecodeString(gotAuth)
	if string(dec) != "{}" {
		t.Fatalf("expected empty json auth, got %q", string(dec))
	}
}

func TestPushCmd_ErrorLineClassification(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/images/lib/app/push", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"denied: requested access to the resource is denied"}` + "\n"))
	})
	stream := newEventStream(context.Background(), fe.connFor(t),
		pushParams{Image: "lib/app"})
	err := (&pushCmd{}).ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_FORBIDDEN" {
		t.Fatalf("want DOCKER_FORBIDDEN, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// docker.exec —— 只测参数验证与错误路径（hijack 需要真实 upgrade）
// -----------------------------------------------------------------------------

func TestExecCmd_MissingParams(t *testing.T) {
	fe := newFakeEngine(t)

	// 缺 id
	stream := newEventStream(context.Background(), fe.connFor(t),
		execParams{Cmd: []string{"ls"}})
	err := (&execCmd{}).ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("expect PARAM_INVALID for missing id, got %v", err)
	}

	// 缺 cmd
	stream2 := newEventStream(context.Background(), fe.connFor(t),
		execParams{ID: "abc"})
	err = (&execCmd{}).ExecuteStream(context.Background(), stream2)
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Fatalf("expect PARAM_INVALID for missing cmd, got %v", err)
	}
}

func TestExecCmd_CreateStageError(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/containers/abc/exec", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such container"}`))
	})
	stream := newEventStream(context.Background(), fe.connFor(t),
		execParams{ID: "abc", Cmd: []string{"true"}})
	err := (&execCmd{}).ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_NOT_FOUND" {
		t.Fatalf("want DOCKER_NOT_FOUND on create stage, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// registryAuth encoding
// -----------------------------------------------------------------------------

func TestEncodeAuth_NilYieldsEmptyJson(t *testing.T) {
	enc, err := encodeAuth(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.URLEncoding.DecodeString(enc)
	if string(raw) != "{}" {
		t.Fatalf("want {}, got %q", raw)
	}
}

func TestEncodeAuth_RoundTrip(t *testing.T) {
	a := &registryAuth{Username: "u", Password: "p", Serveraddress: "reg.io"}
	enc, err := encodeAuth(a)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	var back registryAuth
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Username != "u" || back.Password != "p" || back.Serveraddress != "reg.io" {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}
}

// -----------------------------------------------------------------------------
// pumpProgress: 直接单测（不经 HTTP）覆盖 error 行 & 空行忽略
// -----------------------------------------------------------------------------

func TestPumpProgress_IgnoresEmptyLines(t *testing.T) {
	body := `{"status":"a"}
{}
{"status":"b"}
`
	stream := newEventStream(context.Background(), nil, nil)
	err := pumpProgress(context.Background(), stringReader(body), stream)
	if err != nil {
		t.Fatalf("pumpProgress: %v", err)
	}
	ev := stream.snapshot()
	if len(ev) != 2 {
		t.Fatalf("expected 2 events (empty skipped), got %d", len(ev))
	}
}

func TestPumpProgress_ClassifyNotFound(t *testing.T) {
	body := `{"error":"manifest for foo:bar not found: manifest unknown"}
`
	stream := newEventStream(context.Background(), nil, nil)
	err := pumpProgress(context.Background(), stringReader(body), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_NOT_FOUND" {
		t.Fatalf("want DOCKER_NOT_FOUND, got %v", err)
	}
}

// stringReader 生成一个只读 io.Reader；此处用 strings.NewReader 更简单，但为了保证
// 不引入未使用 import 单独包装。
func stringReader(s string) io.Reader { return strings.NewReader(s) }

// helper 打印 Q 便于失败调试
func _dumpQuery(q map[string][]string) string {
	buf := new(strings.Builder)
	for k, v := range q {
		fmt.Fprintf(buf, "%s=%v ", k, v)
	}
	return buf.String()
}
