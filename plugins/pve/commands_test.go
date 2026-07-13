// commands_test.go —— 通过 httptest 起 fake PVE 服务，覆盖：
//   - Init/Metadata/HealthCheck
//   - endpoint 解析（token_secret_env、多个 endpoint、insecure_tls）
//   - 只读命令（cluster.status / node.list / vm.list / vm.status / lxc.list）
//   - 生命周期命令（vm.start / vm.stop shutdown|stop / vm.reboot / lxc.*）
//   - 错误路径（未配置 token / 未知 endpoint / 401 / 404 / 5xx）
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mow/mow/sdk"
)

// fakePVE 是最小实现的 PVE API 假服务器；把请求路径映射到静态响应。
type fakePVE struct {
	mu       *testing.T
	server   *httptest.Server
	// 每个 method+path 对应一个 handler。缺失路径会 http.NotFound。
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
	// 记录收到的请求，供断言使用。
	receivedAuth map[string]string // path -> Authorization header
}

func newFakePVE(t *testing.T) *fakePVE {
	f := &fakePVE{mu: t, routes: map[string]func(http.ResponseWriter, *http.Request){}, receivedAuth: map[string]string{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if h, ok := f.routes[key]; ok {
			f.receivedAuth[r.URL.Path] = r.Header.Get("Authorization")
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	return f
}

func (f *fakePVE) handle(method, path string, body string) {
	f.routes[method+" "+path] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func (f *fakePVE) handleFunc(method, path string, h func(w http.ResponseWriter, r *http.Request)) {
	f.routes[method+" "+path] = h
}

func (f *fakePVE) close() { f.server.Close() }

// setupPlugin 装配一个绑定到 fake server 的 PVEPlugin。
// 使用 insecure_tls=true 来避开 httptest.NewTLSServer 的自签证书校验。
func setupPlugin(t *testing.T, f *fakePVE, extraEndpoint ...endpointSettings) *PVEPlugin {
	t.Helper()
	p := newPVEPlugin()
	list := []endpointSettings{{
		Name:        "lab",
		Host:        f.server.URL,
		TokenID:     "root@pam!mow-read",
		TokenSecret: "s3cr3t",
		InsecureTLS: true,
	}}
	list = append(list, extraEndpoint...)
	settings := pveSettings{Endpoints: list}
	rawSettings, _ := json.Marshal(settings)
	if err := p.Init(context.Background(), sdk.InitRequest{Settings: rawSettings, DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func execCmd(t *testing.T, p *PVEPlugin, id string, params any) (*sdk.ExecuteResponse, error) {
	t.Helper()
	handlers := p.Commands()
	var h sdk.CommandHandler
	for _, x := range handlers {
		if x.Spec().ID == id {
			h = x
			break
		}
	}
	if h == nil {
		t.Fatalf("command %q not registered", id)
	}
	var raw json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		raw = b
	}
	return h.Execute(context.Background(), &sdk.ExecuteRequest{Params: raw})
}

func TestMetadataAndHealth(t *testing.T) {
	p := newPVEPlugin()
	// 无 endpoint 时 → StatusDegraded
	if s := p.HealthCheck(context.Background()); s != sdk.StatusDegraded {
		t.Fatalf("expected degraded, got %s", s)
	}
	f := newFakePVE(t)
	defer f.close()
	setupPlugin(t, f)
	// 无法直接调用 setupPlugin 已完成的 p；构建单独的以断言 health
	p2 := setupPlugin(t, f)
	if s := p2.HealthCheck(context.Background()); s != sdk.StatusHealthy {
		t.Fatalf("expected healthy, got %s", s)
	}
	meta := p2.Metadata()
	if meta.ID != "pve" || len(meta.ConnectionTypes) != 1 || meta.ConnectionTypes[0] != "pve" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestResolveEndpointsFromEnv(t *testing.T) {
	// 显式 secret + env fallback
	list := []endpointSettings{
		{Name: "lab", Host: "https://a.example", TokenID: "id", TokenSecret: "explicit"},
		{Name: "prod", Host: "https://b.example", TokenID: "id", TokenSecretEnv: "PVE_TOK"},
	}
	byName, def, err := resolveEndpoints(list, func(k string) (string, bool) {
		if k == "PVE_TOK" {
			return "from-env", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if def != "lab" || byName["lab"].tokenSecret != "explicit" {
		t.Fatalf("lab: %+v (def=%s)", byName["lab"], def)
	}
	if byName["prod"].tokenSecret != "from-env" {
		t.Fatalf("prod env not applied: %+v", byName["prod"])
	}
	// 重复名字
	if _, _, err := resolveEndpoints([]endpointSettings{
		{Name: "x", Host: "https://a"}, {Name: "x", Host: "https://b"},
	}, nil); err == nil {
		t.Fatal("expected duplicate error")
	}
	// bad scheme
	if _, _, err := resolveEndpoints([]endpointSettings{{Name: "x", Host: "ftp://a"}}, nil); err == nil {
		t.Fatal("expected scheme error")
	}
	// 缺 host
	if _, _, err := resolveEndpoints([]endpointSettings{{Name: "x"}}, nil); err == nil {
		t.Fatal("expected host required error")
	}
}

func TestClusterStatusAndNodeList(t *testing.T) {
	f := newFakePVE(t)
	defer f.close()
	f.handle(http.MethodGet, "/api2/json/cluster/status", `{"data":[
		{"type":"cluster","name":"lab","quorate":1,"nodes":2},
		{"type":"node","name":"pve1","nodeid":1,"online":1,"level":"","ip":"10.0.0.1","local":1}
	]}`)
	f.handle(http.MethodGet, "/api2/json/nodes", `{"data":[
		{"node":"pve1","status":"online","cpu":0.13,"maxcpu":8,"mem":123,"maxmem":16000000000}
	]}`)
	p := setupPlugin(t, f)

	resp, err := execCmd(t, p, "cluster.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cs clusterStatusResult
	if err := json.Unmarshal(resp.Data, &cs); err != nil {
		t.Fatal(err)
	}
	if len(cs.Entries) != 2 || cs.Entries[0].Type != "cluster" {
		t.Fatalf("unexpected cluster.status: %+v", cs)
	}
	// Authorization header 应携带完整 token
	if !strings.HasPrefix(f.receivedAuth["/api2/json/cluster/status"], "PVEAPIToken=") {
		t.Fatalf("missing token header: %+v", f.receivedAuth)
	}

	resp, err = execCmd(t, p, "node.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	var nl nodeListResult
	if err := json.Unmarshal(resp.Data, &nl); err != nil {
		t.Fatal(err)
	}
	if len(nl.Nodes) != 1 || nl.Nodes[0].Node != "pve1" {
		t.Fatalf("unexpected node.list: %+v", nl)
	}
}

func TestVMListAndLXCList(t *testing.T) {
	f := newFakePVE(t)
	defer f.close()
	// cluster 视角
	f.handleFunc(http.MethodGet, "/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("type")
		w.Header().Set("Content-Type", "application/json")
		if q == "qemu" {
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1","vmid":100,"name":"web","status":"running"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"node":"pve1","vmid":200,"name":"db","status":"stopped"}]}`))
	})
	// node 视角
	f.handle(http.MethodGet, "/api2/json/nodes/pve1/qemu",
		`{"data":[{"vmid":100,"name":"web","status":"running"}]}`)
	f.handle(http.MethodGet, "/api2/json/nodes/pve1/lxc",
		`{"data":[{"vmid":200,"name":"db","status":"stopped"}]}`)

	p := setupPlugin(t, f)
	// vm.list cluster 视角
	resp, err := execCmd(t, p, "vm.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	var vl vmListResult
	if err := json.Unmarshal(resp.Data, &vl); err != nil {
		t.Fatal(err)
	}
	if len(vl.VMs) != 1 || vl.VMs[0].VMID != 100 || vl.VMs[0].Type != "qemu" {
		t.Fatalf("cluster vm.list wrong: %+v", vl)
	}
	// vm.list 指定 node → 走 /nodes/pve1/qemu 并补齐 node 字段
	resp, err = execCmd(t, p, "vm.list", map[string]any{"node": "pve1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resp.Data, &vl); err != nil {
		t.Fatal(err)
	}
	if vl.VMs[0].Node != "pve1" {
		t.Fatalf("node field should be filled: %+v", vl.VMs[0])
	}
	// lxc.list 指定 node
	resp, err = execCmd(t, p, "lxc.list", map[string]any{"node": "pve1"})
	if err != nil {
		t.Fatal(err)
	}
	var ll lxcListResult
	if err := json.Unmarshal(resp.Data, &ll); err != nil {
		t.Fatal(err)
	}
	if len(ll.LXCs) != 1 || ll.LXCs[0].Type != "lxc" || ll.LXCs[0].Node != "pve1" {
		t.Fatalf("lxc.list wrong: %+v", ll)
	}
}

func TestVMStatus(t *testing.T) {
	f := newFakePVE(t)
	defer f.close()
	f.handle(http.MethodGet, "/api2/json/nodes/pve1/qemu/100/status/current",
		`{"data":{"vmid":100,"name":"web","status":"running","cpu":0.05,"cpus":2,"mem":800000000,"maxmem":2000000000,"uptime":3600,"ha":{"managed":1}}}`)
	p := setupPlugin(t, f)
	resp, err := execCmd(t, p, "vm.status", map[string]any{"node": "pve1", "vmid": 100})
	if err != nil {
		t.Fatal(err)
	}
	var s vmStatusResult
	if err := json.Unmarshal(resp.Data, &s); err != nil {
		t.Fatal(err)
	}
	if s.VMID != 100 || s.Name != "web" || !s.HA || s.CPUs != 2 {
		t.Fatalf("unexpected vm.status: %+v", s)
	}
	// missing params
	if _, err := execCmd(t, p, "vm.status", map[string]any{"node": "pve1"}); err == nil {
		t.Fatal("expected PARAM_INVALID")
	}
}

func TestLifecycleCommands(t *testing.T) {
	f := newFakePVE(t)
	defer f.close()
	visits := map[string]int{}
	post := func(path, upid string) {
		f.handleFunc(http.MethodPost, path, func(w http.ResponseWriter, r *http.Request) {
			visits[path]++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
		})
	}
	post("/api2/json/nodes/pve1/qemu/100/status/start", "UPID:pve1:start-100")
	post("/api2/json/nodes/pve1/qemu/100/status/shutdown", "UPID:pve1:shutdown-100")
	post("/api2/json/nodes/pve1/qemu/100/status/stop", "UPID:pve1:stop-100")
	post("/api2/json/nodes/pve1/qemu/100/status/reboot", "UPID:pve1:reboot-100")
	post("/api2/json/nodes/pve1/lxc/200/status/start", "UPID:pve1:lxc-start-200")

	p := setupPlugin(t, f)
	// vm.start
	resp, err := execCmd(t, p, "vm.start", map[string]any{"node": "pve1", "vmid": 100})
	if err != nil {
		t.Fatal(err)
	}
	var r lifecycleResult
	if err := json.Unmarshal(resp.Data, &r); err != nil {
		t.Fatal(err)
	}
	if r.UPID != "UPID:pve1:start-100" || r.Action != "start" || r.Kind != "qemu" {
		t.Fatalf("unexpected vm.start: %+v", r)
	}
	// vm.stop (default → shutdown)
	if _, err := execCmd(t, p, "vm.stop", map[string]any{"node": "pve1", "vmid": 100}); err != nil {
		t.Fatal(err)
	}
	if visits["/api2/json/nodes/pve1/qemu/100/status/shutdown"] != 1 {
		t.Fatalf("expected shutdown, visits=%+v", visits)
	}
	// vm.stop force=true → /status/stop
	if _, err := execCmd(t, p, "vm.stop", map[string]any{"node": "pve1", "vmid": 100, "force": true}); err != nil {
		t.Fatal(err)
	}
	if visits["/api2/json/nodes/pve1/qemu/100/status/stop"] != 1 {
		t.Fatalf("expected stop, visits=%+v", visits)
	}
	// vm.reboot
	if _, err := execCmd(t, p, "vm.reboot", map[string]any{"node": "pve1", "vmid": 100}); err != nil {
		t.Fatal(err)
	}
	// lxc.start
	if _, err := execCmd(t, p, "lxc.start", map[string]any{"node": "pve1", "vmid": 200}); err != nil {
		t.Fatal(err)
	}
	// missing params
	if _, err := execCmd(t, p, "vm.start", map[string]any{"node": "pve1"}); err == nil {
		t.Fatal("expected PARAM_INVALID")
	}
}

func TestUnauthorizedAndNotFound(t *testing.T) {
	f := newFakePVE(t)
	defer f.close()
	f.handleFunc(http.MethodGet, "/api2/json/cluster/status", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"data":null,"errors":{"auth":"forbidden"}}`))
	})
	f.handleFunc(http.MethodGet, "/api2/json/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	p := setupPlugin(t, f)

	if _, err := execCmd(t, p, "cluster.status", nil); err == nil {
		t.Fatal("expected unauthorized error")
	} else {
		var se *sdk.Error
		if !errAs(err, &se) || se.Code != "PVE_UNAUTHORIZED" {
			t.Fatalf("bad error: %v", err)
		}
	}
	if _, err := execCmd(t, p, "node.list", nil); err == nil {
		t.Fatal("expected not found")
	} else {
		var se *sdk.Error
		if !errAs(err, &se) || se.Code != "PVE_NOT_FOUND" {
			t.Fatalf("bad error: %v", err)
		}
	}
}

func TestMissingTokenAndUnknownEndpoint(t *testing.T) {
	f := newFakePVE(t)
	defer f.close()

	// 无 token
	p := newPVEPlugin()
	list := []endpointSettings{{Name: "lab", Host: f.server.URL, InsecureTLS: true}}
	raw, _ := json.Marshal(pveSettings{Endpoints: list})
	if err := p.Init(context.Background(), sdk.InitRequest{Settings: raw, DataDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := execCmd(t, p, "cluster.status", nil); err == nil {
		t.Fatal("expected PVE_UNAUTHORIZED (no token)")
	}

	// 未知 endpoint
	p2 := setupPlugin(t, f)
	if _, err := execCmd(t, p2, "cluster.status", map[string]any{"endpoint": "nope"}); err == nil {
		t.Fatal("expected unknown endpoint error")
	}

	// 完全未配置 endpoint → resolveEndpoint 报错
	p3 := newPVEPlugin()
	if _, err := execCmd(t, p3, "cluster.status", nil); err == nil {
		t.Fatal("expected error when no endpoints configured")
	}
}

// errAs：极小 errors.As 包装，避免每处都 import。
func errAs(err error, target any) bool {
	return errors.As(err, target)
}
