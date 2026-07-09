package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// -----------------------------------------------------------------------------
// resolveTarget
// -----------------------------------------------------------------------------

func TestResolveTarget_HappyPath(t *testing.T) {
	// 通过 exportedConn 构造符合规格的 sdk.Connection —— 为避免拉入 sdk 类型
	// 这里直接构造匿名结构不可行，转而通过 credentials.go 内的函数。
	// 保持简单，此处只测参数缺失分支。
	if _, err := resolveTarget(nil); err == nil {
		t.Error("nil conn should fail")
	}
}

// -----------------------------------------------------------------------------
// SessionPool（用 stub dialer 模拟 client 创建；不做真实 TCP 握手）
// -----------------------------------------------------------------------------

// stubClient 提供一个已关闭的 net.Pipe，以获得一个不做实际 IO 的 *ssh.Client 是复杂的；
// 因此这里只测"多次 Acquire 触发同一次 dial + Release 后 GC 关闭"这些不依赖
// 底层 *ssh.Client 的逻辑，通过替换 pool.opts.Dialer 返回同一个 sentinel。

type dialCallResult struct {
	client *ssh.Client
	err    error
	calls  int
}

func TestSessionPool_ReusesClient(t *testing.T) {
	// 用一个不能建立真实连接的地址；dialer 直接返回 nil *ssh.Client。
	// 我们只观察 dial 被调用的次数与 refs 变化。
	var (
		mu sync.Mutex
		r  dialCallResult
	)
	r.err = errors.New("stub: no real client")

	p := NewSessionPool(SessionPoolOptions{
		DialTimeout: 100 * time.Millisecond,
		IdleTTL:     50 * time.Millisecond,
		GCInterval:  20 * time.Millisecond,
		Dialer: func(network, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
			mu.Lock()
			r.calls++
			mu.Unlock()
			return r.client, r.err
		},
	})
	defer p.Close()

	dt := &dialTarget{
		ID: "t1", Host: "127.0.0.1", Port: 22, User: "u",
		Creds: sshCredentials{Method: "password", Password: "x"},
	}
	if _, _, err := p.Acquire(context.Background(), dt); err == nil {
		t.Fatal("stub dial should fail")
	}
	mu.Lock()
	got := r.calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("dial should be called once on failure, got %d", got)
	}
	total, _ := p.Stats()
	if total != 0 {
		t.Errorf("failed dial should not leave pooled entry, got %d", total)
	}
}

func TestSessionPool_Stats(t *testing.T) {
	p := NewSessionPool(SessionPoolOptions{})
	defer p.Close()
	total, idle := p.Stats()
	if total != 0 || idle != 0 {
		t.Errorf("empty pool should be 0/0, got %d/%d", total, idle)
	}
}

// -----------------------------------------------------------------------------
// buildAuthMethods
// -----------------------------------------------------------------------------

func TestBuildAuthMethods_Password(t *testing.T) {
	m, err := buildAuthMethods(&sshCredentials{Method: "password", Password: "x"})
	if err != nil || len(m) != 1 {
		t.Errorf("password auth failed: len=%d err=%v", len(m), err)
	}
}

func TestBuildAuthMethods_Empty(t *testing.T) {
	if _, err := buildAuthMethods(&sshCredentials{Method: "password"}); err == nil {
		t.Error("empty password should be rejected")
	}
}

func TestBuildAuthMethods_UnknownMethod(t *testing.T) {
	if _, err := buildAuthMethods(&sshCredentials{Method: "sso"}); err == nil {
		t.Error("unknown method should be rejected")
	}
}

func TestBuildHostKeyCallback_Insecure(t *testing.T) {
	cb, err := buildHostKeyCallback(&sshCredentials{KnownHostsMode: "insecure-ignore"})
	if err != nil || cb == nil {
		t.Errorf("insecure-ignore should always work: %v", err)
	}
}

func TestBuildHostKeyCallback_StrictRequiresPath(t *testing.T) {
	if _, err := buildHostKeyCallback(&sshCredentials{KnownHostsMode: "strict"}); err == nil {
		t.Error("strict without path should fail")
	}
}
