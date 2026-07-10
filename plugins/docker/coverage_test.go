// coverage_test.go —— v0.3.1 稳定性补丁：
// 补齐 plugins/docker 中低成本高价值的错误路径 / 元信息 / helper 覆盖，
// 让整包覆盖率从 59.6% 抬到 v0.3.1 目标的 70%+。
//
// 只覆盖能纯单测 / httptest / 直接函数调用 就能触达的点：
//   - Metadata / Commands / HealthCheck 元信息
//   - 每个 CommandHandler 的 Spec()
//   - 每个 Execute vs ExecuteStream "不支持" 分支
//   - client.go 里的错误码映射 / 传输错误分类 / TLS 构造
//   - exec.go 里 TLS + npipe 提前拒绝路径
//   - credentials.go 边界（无 host、npipe）
//   - decodeParams / isSDKError 小工具
//
// hijack 类真实拨号 (dialHijack / relayInbound) 需要真 daemon，
// 交给 tests/e2e 的 daemon E2E 覆盖，不在此重复。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// Metadata / Commands / HealthCheck
// -----------------------------------------------------------------------------

func TestPluginMetadataAndCommands(t *testing.T) {
	p := newDockerPlugin()
	md := p.Metadata()
	if md.ID != "docker" || len(md.ConnectionTypes) == 0 || md.ConnectionTypes[0] != "docker" {
		t.Fatalf("unexpected metadata: %+v", md)
	}
	if err := p.Init(context.Background(), sdk.InitRequest{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if s := p.HealthCheck(context.Background()); s != sdk.StatusHealthy {
		t.Fatalf("HealthCheck = %v", s)
	}
	cmds := p.Commands()
	// v0.3 三个阶段共 13 个命令：list / inspect / start / stop / restart / logs /
	// rm / pull / push / exec / images / volumes / networks
	if len(cmds) != 13 {
		t.Fatalf("expected 13 commands, got %d", len(cmds))
	}
	// 每个命令必须有非空 ID / 声明 docker 连接类型
	seen := map[string]bool{}
	for _, c := range cmds {
		spec := c.Spec()
		if spec.ID == "" {
			t.Fatalf("empty spec ID: %#v", c)
		}
		if spec.ConnectionType != "docker" {
			t.Fatalf("cmd %s: connection type = %q", spec.ID, spec.ConnectionType)
		}
		if seen[spec.ID] {
			t.Fatalf("duplicate command id: %s", spec.ID)
		}
		seen[spec.ID] = true
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Execute vs ExecuteStream 的 "不支持" 分支
// -----------------------------------------------------------------------------

// TestStreamCommandsRejectExecute 验证：所有流式命令的 Execute() 都返回 ErrNotSupported。
// 反过来，所有非流式命令的 ExecuteStream() 也应返回 ErrNotSupported。
func TestStreamCommandsRejectExecute(t *testing.T) {
	// 流式命令：logs / pull / push / exec
	streamOnly := []sdk.CommandHandler{
		&logsCmd{}, &pullCmd{}, &pushCmd{}, &execCmd{},
	}
	for _, c := range streamOnly {
		_, err := c.Execute(context.Background(), &sdk.ExecuteRequest{})
		if !errors.Is(err, sdk.ErrNotSupported) {
			t.Fatalf("%s.Execute expected ErrNotSupported, got %v", c.Spec().ID, err)
		}
	}
	// 非流式命令：list / inspect / start / stop / restart / rm / images / volumes / networks
	nonStream := []sdk.CommandHandler{
		&listCmd{}, &inspectCmd{}, &startCmd{}, &stopCmd{}, &restartCmd{},
		&rmCmd{}, &imagesCmd{}, &volumesCmd{}, &networksCmd{},
	}
	for _, c := range nonStream {
		// 传一个满足 sdk.Stream 接口的最小 stub 会太重；直接调用即可，
		// 大多数实现在函数体第一行就会返回 ErrNotSupported。
		type esStub interface {
			ExecuteStream(context.Context, sdk.Stream) error
		}
		if es, ok := c.(esStub); ok {
			err := es.ExecuteStream(context.Background(), nil)
			if !errors.Is(err, sdk.ErrNotSupported) {
				t.Fatalf("%s.ExecuteStream expected ErrNotSupported, got %v", c.Spec().ID, err)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// client.go：statusCodeToErrorCode / mapTransportError / buildTLSConfig
// -----------------------------------------------------------------------------

func TestStatusCodeToErrorCode_Table(t *testing.T) {
	cases := map[int]string{
		http.StatusNotFound:            "DOCKER_NOT_FOUND",
		http.StatusConflict:            "DOCKER_CONFLICT",
		http.StatusNotModified:         "DOCKER_NOT_MODIFIED",
		http.StatusBadRequest:          "DOCKER_BAD_REQUEST",
		http.StatusUnauthorized:        "DOCKER_UNAUTHORIZED",
		http.StatusForbidden:           "DOCKER_UNAUTHORIZED",
		http.StatusInternalServerError: "DOCKER_ENGINE_ERROR",
		http.StatusBadGateway:          "DOCKER_ENGINE_ERROR", // 任何 >=500 都归 engine error
		418:                            "DOCKER_HTTP_418",     // 其它 → HTTP_xxx
	}
	for code, want := range cases {
		if got := statusCodeToErrorCode(code); got != want {
			t.Errorf("statusCodeToErrorCode(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestMapTransportError_CanceledTimeoutOther(t *testing.T) {
	// nil 保持 nil
	if err := mapTransportError(nil); err != nil {
		t.Fatalf("nil should stay nil, got %v", err)
	}
	// CANCELED
	err := mapTransportError(context.Canceled)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "CANCELED" {
		t.Fatalf("canceled: got %v", err)
	}
	// TIMEOUT
	err = mapTransportError(context.DeadlineExceeded)
	if !errors.As(err, &se) || se.Code != "TIMEOUT" {
		t.Fatalf("timeout: got %v", err)
	}
	// 其它 → DOCKER_DIAL_FAILED + retryable
	err = mapTransportError(errors.New("boom"))
	if !errors.As(err, &se) || se.Code != "DOCKER_DIAL_FAILED" || !se.Retryable {
		t.Fatalf("dial failed: got %v (retryable=%v)", err, se.Retryable)
	}
}

// 测试 TLS 构造：包含 PEM 校验失败 / X509 KeyPair 失败 / 成功三条路径。
func TestBuildTLSConfig_ErrorsAndSuccess(t *testing.T) {
	// bad CA
	_, err := buildTLSConfig(&dockerCredentials{TLSCA: "not-a-pem"}, "docker.local:2376")
	if err == nil {
		t.Fatal("expected bad CA error")
	}
	// good CA + bad key/cert
	_, err = buildTLSConfig(&dockerCredentials{
		TLSCA:   testPEM,
		TLSCert: "not-a-cert",
		TLSKey:  "not-a-key",
	}, "docker.local:2376")
	if err == nil {
		t.Fatal("expected bad key/cert error")
	}
	// 成功路径：CA + 匹配 key/cert
	cfg, err := buildTLSConfig(&dockerCredentials{
		TLSCA:   testPEM,
		TLSCert: testPEM,
		TLSKey:  testKey,
	}, "docker.local:2376")
	if err != nil {
		t.Fatalf("valid tls config: %v", err)
	}
	if cfg.ServerName != "docker.local" || cfg.MinVersion == 0 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	// addr 不含 port 时使用整串作为 ServerName
	cfg2, err := buildTLSConfig(&dockerCredentials{
		TLSCA: testPEM, TLSCert: testPEM, TLSKey: testKey,
	}, "just-a-name")
	if err != nil {
		t.Fatalf("no-port tls config: %v", err)
	}
	if cfg2.ServerName != "just-a-name" {
		t.Fatalf("ServerName = %q", cfg2.ServerName)
	}
}

// -----------------------------------------------------------------------------
// client.go：newEngineClient 的分支
// -----------------------------------------------------------------------------

func TestNewEngineClient_NpipeUnsupported(t *testing.T) {
	_, err := newEngineClient(&dialTarget{
		Scheme: "npipe", NetAddr: `\\.\pipe\docker_engine`,
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "DOCKER_NPIPE_UNSUPPORTED" {
		t.Fatalf("expected DOCKER_NPIPE_UNSUPPORTED, got %v", err)
	}
}

func TestNewEngineClient_UnknownScheme(t *testing.T) {
	_, err := newEngineClient(&dialTarget{Scheme: "quic", NetAddr: "docker.local:2376"})
	if err == nil {
		t.Fatal("expected error for unknown scheme")
	}
}

func TestNewEngineClient_UnixPathBuild(t *testing.T) {
	// 只验证能构造出 client；实际拨号在 e2e 覆盖
	c, err := newEngineClient(&dialTarget{Scheme: "unix", NetAddr: "/var/run/docker.sock"})
	if err != nil {
		t.Fatalf("unix client build: %v", err)
	}
	if c == nil {
		t.Fatal("unix client is nil")
	}
	c.closeIdle()
}

// -----------------------------------------------------------------------------
// exec.go：TLS / npipe pre-guards
// -----------------------------------------------------------------------------

func TestExecCmd_TLSAndNpipeRejected(t *testing.T) {
	// 直接调 ExecuteStream，构造一个 conn 具备 tls_verify=true 的 credentials；
	// 不用真的连到 Engine，因为 pre-guard 在 newEngineClient 之前拦截。
	rawTLS, _ := json.Marshal(dockerCredentials{
		Host: "tcp://docker.local:2376", TLSVerify: true,
	})
	rawNpipe, _ := json.Marshal(dockerCredentials{Host: "npipe:////./pipe/docker_engine"})

	params, _ := json.Marshal(execParams{ID: "abc", Cmd: []string{"echo", "hi"}})

	// TLS 分支
	{
		st := newFakeStream(context.Background(),
			&sdk.Connection{ID: "dk", Type: "docker", Credentials: rawTLS}, execParams{})
		// fakeStream.Params(dst) 走 json.Unmarshal(raw, dst)，
		// 而我们 rawParams 是 execParams{} 的 marshal → id/cmd 空；
		// 因此需要重建 stream 用真正的 params。
		st = newFakeStream(context.Background(),
			&sdk.Connection{ID: "dk", Type: "docker", Credentials: rawTLS},
			json.RawMessage(params))
		err := (&execCmd{}).ExecuteStream(context.Background(), st)
		var se *sdk.Error
		if !errors.As(err, &se) || se.Code != "DOCKER_EXEC_TLS_UNSUPPORTED" {
			t.Fatalf("TLS: expected DOCKER_EXEC_TLS_UNSUPPORTED, got %v", err)
		}
	}
	// npipe 分支
	{
		st := newFakeStream(context.Background(),
			&sdk.Connection{ID: "dk", Type: "docker", Credentials: rawNpipe},
			json.RawMessage(params))
		err := (&execCmd{}).ExecuteStream(context.Background(), st)
		var se *sdk.Error
		if !errors.As(err, &se) || se.Code != "DOCKER_EXEC_NPIPE_UNSUPPORTED" {
			t.Fatalf("npipe: expected DOCKER_EXEC_NPIPE_UNSUPPORTED, got %v", err)
		}
	}
}

// -----------------------------------------------------------------------------
// credentials.go：splitHost 的错误分支
// （resolveTarget 空 host 已在 credentials_test.go 覆盖，此处不重复）
// -----------------------------------------------------------------------------

func TestSplitHost_UnknownScheme(t *testing.T) {
	if _, _, err := splitHost("weird://x"); err == nil {
		t.Fatal("expected error for weird scheme")
	}
}

// -----------------------------------------------------------------------------
// commands.go：decodeParams / isSDKError
// -----------------------------------------------------------------------------

func TestDecodeParams_ErrorBubblesUp(t *testing.T) {
	var out listParams
	err := decodeParams(json.RawMessage(`{"all": "yes-please"}`), &out)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestDecodeParams_NilParamsOK(t *testing.T) {
	var out listParams
	if err := decodeParams(nil, &out); err != nil {
		t.Fatalf("nil params should be OK: %v", err)
	}
}

func TestIsSDKError(t *testing.T) {
	var target *sdk.Error
	se := sdk.NewError("X", "x", nil)
	if !isSDKError(se, &target) || target == nil || target.Code != "X" {
		t.Fatal("sdk.Error should be recognized")
	}
	target = nil
	if isSDKError(errors.New("plain"), &target) {
		t.Fatal("plain error should not be recognized")
	}
	target = nil
	if isSDKError(nil, &target) {
		t.Fatal("nil should not be recognized")
	}
}

// -----------------------------------------------------------------------------
// client.go：do / postJSON 的 5xx / 非 JSON body 路径
// -----------------------------------------------------------------------------

// Test5xxReturnsEngineError 让 fake engine 直接 500，验证客户端把响应包成 DOCKER_ENGINE_ERROR。
func TestClient_5xxReturnsEngineError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	c, err := newEngineClient(&dialTarget{Scheme: "tcp", NetAddr: u.Host})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.closeIdle()

	// 走 getJSON 触发 do → decodeEngineError → statusCodeToErrorCode
	var out struct{}
	err = c.getJSON(context.Background(), "/anything", nil, &out)
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected sdk.Error, got %T %v", err, err)
	}
	if se.Code != "DOCKER_ENGINE_ERROR" {
		t.Fatalf("code = %q", se.Code)
	}
}

// TestClient_PostJSONNilDstAndBadBody 覆盖 postJSON 的两条剩余分支：
//   - body 为不可 JSON 化对象 → ENCODE_FAILED
//   - dst=nil 时静默丢弃响应
func TestClient_PostJSONNilDstAndBadBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	c, err := newEngineClient(&dialTarget{Scheme: "tcp", NetAddr: u.Host})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.closeIdle()

	// dst=nil 成功路径
	if err := c.postJSON(context.Background(), "/anything", nil, map[string]string{"k": "v"}, nil); err != nil {
		t.Fatalf("postJSON nil dst: %v", err)
	}
	// 不可序列化 body → ENCODE_FAILED
	// channel 类型 json.Marshal 会失败
	badBody := make(chan int)
	err = c.postJSON(context.Background(), "/anything", nil, badBody, nil)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "ENCODE_FAILED" {
		t.Fatalf("expected ENCODE_FAILED, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// exec.go：参数校验 + nil connection
// -----------------------------------------------------------------------------

func TestExecCmd_ConnectionAndParamValidation(t *testing.T) {
	// nil connection
	{
		st := newFakeStream(context.Background(), nil, json.RawMessage(`{}`))
		err := (&execCmd{}).ExecuteStream(context.Background(), st)
		if !errors.Is(err, sdk.ErrConnectionRequired) {
			t.Fatalf("expected ErrConnectionRequired, got %v", err)
		}
	}
	// bad params: cmd 缺失
	{
		raw, _ := json.Marshal(dockerCredentials{Host: "unix:///var/run/docker.sock"})
		st := newFakeStream(context.Background(),
			&sdk.Connection{ID: "dk", Type: "docker", Credentials: raw},
			json.RawMessage(`{"id":"abc"}`))
		err := (&execCmd{}).ExecuteStream(context.Background(), st)
		var se *sdk.Error
		if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
			t.Fatalf("expected PARAM_INVALID for missing cmd, got %v", err)
		}
	}
	// bad params: id 缺失
	{
		raw, _ := json.Marshal(dockerCredentials{Host: "unix:///var/run/docker.sock"})
		st := newFakeStream(context.Background(),
			&sdk.Connection{ID: "dk", Type: "docker", Credentials: raw},
			json.RawMessage(`{"cmd":["echo","hi"]}`))
		err := (&execCmd{}).ExecuteStream(context.Background(), st)
		var se *sdk.Error
		if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
			t.Fatalf("expected PARAM_INVALID for missing id, got %v", err)
		}
	}
	// bad params: 反序列化失败
	{
		raw, _ := json.Marshal(dockerCredentials{Host: "unix:///var/run/docker.sock"})
		st := newFakeStream(context.Background(),
			&sdk.Connection{ID: "dk", Type: "docker", Credentials: raw},
			json.RawMessage(`{"id":123}`)) // id 类型错误
		err := (&execCmd{}).ExecuteStream(context.Background(), st)
		var se *sdk.Error
		if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
			t.Fatalf("expected PARAM_INVALID for decode error, got %v", err)
		}
	}
}

// -----------------------------------------------------------------------------
// logs.go：mapReadErr 全分支
// -----------------------------------------------------------------------------

func TestMapReadErr_Table(t *testing.T) {
	// nil → nil
	if err := mapReadErr(context.Background(), nil); err != nil {
		t.Fatalf("nil should stay nil, got %v", err)
	}
	// io.EOF → nil（流正常结束）
	if err := mapReadErr(context.Background(), io.EOF); err != nil {
		t.Fatalf("EOF should be treated as nil, got %v", err)
	}
	// ctx canceled → CANCELED
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mapReadErr(ctx, errors.New("read failed"))
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "CANCELED" {
		t.Fatalf("canceled: got %v", err)
	}
	// ctx deadline exceeded → TIMEOUT
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	<-dctx.Done()
	err = mapReadErr(dctx, errors.New("read failed"))
	if !errors.As(err, &se) || se.Code != "TIMEOUT" {
		t.Fatalf("timeout: got %v", err)
	}
	// 普通读错误 → DOCKER_READ_FAILED
	err = mapReadErr(context.Background(), errors.New("io broken"))
	if !errors.As(err, &se) || se.Code != "DOCKER_READ_FAILED" {
		t.Fatalf("generic read: got %v", err)
	}
}

// -----------------------------------------------------------------------------
// images.go：classifyRegistryError
// -----------------------------------------------------------------------------

func TestClassifyRegistryError_Table(t *testing.T) {
	cases := []struct {
		msg  string
		code string
	}{
		{"unauthorized: authentication required", "DOCKER_UNAUTHORIZED"},
		{"denied: requested access to the resource is denied", "DOCKER_FORBIDDEN"},
		{"manifest for foo/bar:baz not found", "DOCKER_NOT_FOUND"},
		{"manifest unknown", "DOCKER_NOT_FOUND"},
		{"random other message", "DOCKER_REGISTRY_ERROR"},
	}
	for _, c := range cases {
		got := classifyRegistryError(c.msg)
		if got != c.code {
			t.Errorf("classifyRegistryError(%q) = %q, want %q", c.msg, got, c.code)
		}
	}
}

// TestIntToStr 覆盖 exec.go 里的小工具函数（0 / 正数 / 负数）。
func TestIntToStr(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 42: "42", -7: "-7", 123456: "123456"}
	for in, want := range cases {
		if got := intToStr(in); got != want {
			t.Errorf("intToStr(%d) = %q, want %q", in, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// PEM 素材（自签 CA + 匹配私钥）—— 仅用于 buildTLSConfig 单测，
// 不用来做真实握手。生成方式：openssl req -x509 -newkey rsa:2048 ...
// 每次重跑测试都能通过静态解析，不依赖时间。
// -----------------------------------------------------------------------------

const testPEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----
`

const testKey = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIIrYSSNQFaA2Hwf1duRSxKtLYX5CB04fSeQ6tF1aY/PuoAoGCCqGSM49
AwEHoUQDQgAEPR3tU2Fta9ktY+6P9G0cWO+0kETA6SFs38GecTyudlHz6xvCdz8q
EKTcWGekdmdDPsHloRNtsiCa697B2O9IFA==
-----END EC PRIVATE KEY-----
`
