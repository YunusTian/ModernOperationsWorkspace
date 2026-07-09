package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSessionPool_ConcurrentAcquireRelease 用大量并发 goroutine
// 反复 Acquire / Release 同一个 key，压测引用计数与 map 访问。
//
// 说明：
//   - 用 stub dialer 返回 nil 假 *ssh.Client（本测试不做真正 IO）
//   - 观察：所有 goroutine 结束后，Stats 显示池内为 1 且 idle=1
//     （所有 Release 后 refs 归零；未过 IdleTTL 不会被 GC）
//   - 若 refs / lastUsed / map 存在竞态，此用例配合 `go test -race`
//     会直接抛出数据竞争报告。
func TestSessionPool_ConcurrentAcquireRelease(t *testing.T) {
	dialCount := 0
	var mu sync.Mutex

	p := NewSessionPool(SessionPoolOptions{
		DialTimeout: time.Second,
		IdleTTL:     5 * time.Second,
		GCInterval:  50 * time.Millisecond,
		Dialer: func(network, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
			mu.Lock()
			dialCount++
			mu.Unlock()
			// nil client 在池内是一个"占位符"——本测试不做真正 IO，
			// pool 的 Close/Evict 均已对 nil client 做防御。
			return nil, nil
		},
	})
	defer p.Close()

	dt := &dialTarget{
		ID: "t", Host: "1.2.3.4", Port: 22, User: "u",
		Creds: sshCredentials{Method: "password", Password: "x", KnownHostsMode: "insecure-ignore"},
	}

	const (
		workers = 32
		rounds  = 50
	)
	var wg sync.WaitGroup
	ctx := context.Background()
	errCh := make(chan error, workers*rounds)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				_, key, err := p.Acquire(ctx, dt)
				if err != nil {
					errCh <- err
					return
				}
				p.Release(key)
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("acquire failed: %v", err)
		}
	}

	// 应仅拨号一次（并发下允许极小概率多次，但典型情况下 == 1）
	mu.Lock()
	got := dialCount
	mu.Unlock()
	if got < 1 {
		t.Fatalf("dial should be called at least once, got %d", got)
	}
	if got > 4 {
		// 高并发下拨号竞态窗口通常 <=2；超过 4 说明池语义可能有问题
		t.Errorf("dial called too many times (want <=4), got %d", got)
	}

	total, idle := p.Stats()
	if total != 1 {
		t.Errorf("pool total want 1, got %d", total)
	}
	if idle != 1 {
		t.Errorf("pool idle want 1, got %d", idle)
	}
}

// TestSessionPool_EvictConcurrent 覆盖 Evict 与 Release 的并发冲突。
func TestSessionPool_EvictConcurrent(t *testing.T) {
	p := NewSessionPool(SessionPoolOptions{
		DialTimeout: time.Second,
		IdleTTL:     time.Second,
		GCInterval:  100 * time.Millisecond,
		Dialer: func(network, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
			return nil, nil
		},
	})
	defer p.Close()

	dt := &dialTarget{
		ID: "t", Host: "1.2.3.4", Port: 22, User: "u",
		Creds: sshCredentials{Method: "password", Password: "x", KnownHostsMode: "insecure-ignore"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, key, err := p.Acquire(context.Background(), dt)
			if err != nil && !errors.Is(err, context.Canceled) {
				// Evict 时并发 Acquire 应重新拨号；语义上不应报错。
				return
			}
			p.Evict(key)
			p.Release(key)
		}()
	}
	wg.Wait()

	// 不应发生 panic / 死锁。到达这里即通过。
}
