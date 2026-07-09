package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// -----------------------------------------------------------------------------
// SessionPool
// -----------------------------------------------------------------------------
//
// 语义（v0.1）：
//   - Key = "<targetID>@<host>:<port>"
//   - 一个 Key 对应一个 *ssh.Client（长连接），可并发 NewSession
//   - Acquire 增加引用计数并返回 client；使用者用完调用 Release
//   - 空闲（refs=0）且距上次 Release 超过 IdleTTL 的 client，将被后台 GC 关闭
//   - Close 关闭全部 client
//
// 这里 **不** 用 sync.Map，因为需要按 key 做"检查-创建"的原子性；
// 用 sync.Mutex + map 更直接。sync.Map 更适合"读多写少 + 无需 CAS"的场景。

// PooledClient 是池内 *ssh.Client 的运行时视图。
type PooledClient struct {
	Key    string
	Client *ssh.Client

	refs      int
	lastUsed  time.Time
	closed    bool
}

// SessionPoolOptions 是 SessionPool 的构造参数。
type SessionPoolOptions struct {
	// DialTimeout 是 TCP + SSH 握手的最大耗时。默认 15s。
	DialTimeout time.Duration
	// IdleTTL 是空闲连接的最大存活时长。默认 5 分钟。
	IdleTTL time.Duration
	// GCInterval 是空闲 GC 的扫描间隔。默认 30s。
	GCInterval time.Duration

	// Dialer 允许在测试中注入自定义拨号器。
	// 生产路径为 nil，走默认 ssh.Dial。
	Dialer func(network, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error)
}

// SessionPool 维护 targetKey → *ssh.Client 的复用。
type SessionPool struct {
	opts SessionPoolOptions

	mu      sync.Mutex
	clients map[string]*PooledClient

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewSessionPool 构造一个 SessionPool 并启动后台 GC。
func NewSessionPool(opts SessionPoolOptions) *SessionPool {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 5 * time.Minute
	}
	if opts.GCInterval <= 0 {
		opts.GCInterval = 30 * time.Second
	}
	p := &SessionPool{
		opts:    opts,
		clients: map[string]*PooledClient{},
		stopCh:  make(chan struct{}),
	}
	p.wg.Add(1)
	go p.gcLoop()
	return p
}

// Close 关闭全部 client 并停止 GC。
func (p *SessionPool) Close() {
	select {
	case <-p.stopCh:
		return
	default:
	}
	close(p.stopCh)
	p.wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	for k, pc := range p.clients {
		if pc.Client != nil {
			_ = pc.Client.Close()
		}
		pc.closed = true
		delete(p.clients, k)
	}
}

// Acquire 拿到一个可用 *ssh.Client。若不存在，则调用 dial 建立。
// 使用者必须成对调用 Release。
func (p *SessionPool) Acquire(ctx context.Context, dt *dialTarget) (*ssh.Client, string, error) {
	key := fmt.Sprintf("%s@%s:%d", dt.ID, dt.Host, dt.Port)

	p.mu.Lock()
	if pc, ok := p.clients[key]; ok && !pc.closed {
		pc.refs++
		pc.lastUsed = time.Now()
		p.mu.Unlock()
		return pc.Client, key, nil
	}
	p.mu.Unlock()

	client, err := p.dial(ctx, dt)
	if err != nil {
		return nil, key, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// 竞争窗口：并发下可能已被另一个 Acquire 建好。
	if pc, ok := p.clients[key]; ok && !pc.closed {
		if client != nil {
			_ = client.Close()
		}
		pc.refs++
		pc.lastUsed = time.Now()
		return pc.Client, key, nil
	}
	p.clients[key] = &PooledClient{
		Key:      key,
		Client:   client,
		refs:     1,
		lastUsed: time.Now(),
	}
	return client, key, nil
}

// Release 归还一次引用。计数归零时不会立即关闭——由 GC 决定。
func (p *SessionPool) Release(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pc, ok := p.clients[key]
	if !ok {
		return
	}
	if pc.refs > 0 {
		pc.refs--
	}
	pc.lastUsed = time.Now()
}

// Evict 强制关闭一个 client（例如底层出现 EOF 时）。
func (p *SessionPool) Evict(key string) {
	p.mu.Lock()
	pc, ok := p.clients[key]
	if ok {
		delete(p.clients, key)
	}
	p.mu.Unlock()
	if ok && pc.Client != nil {
		_ = pc.Client.Close()
	}
}

// Stats 返回 (总数, 空闲数)，供可观测面板使用。
func (p *SessionPool) Stats() (total, idle int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	total = len(p.clients)
	for _, pc := range p.clients {
		if pc.refs == 0 {
			idle++
		}
	}
	return
}

// -----------------------------------------------------------------------------
// GC
// -----------------------------------------------------------------------------

func (p *SessionPool) gcLoop() {
	defer p.wg.Done()
	t := time.NewTicker(p.opts.GCInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.sweep()
		}
	}
}

func (p *SessionPool) sweep() {
	now := time.Now()
	var toClose []*PooledClient
	p.mu.Lock()
	for k, pc := range p.clients {
		if pc.refs == 0 && now.Sub(pc.lastUsed) > p.opts.IdleTTL {
			pc.closed = true
			toClose = append(toClose, pc)
			delete(p.clients, k)
		}
	}
	p.mu.Unlock()
	for _, pc := range toClose {
		if pc.Client != nil {
			_ = pc.Client.Close()
		}
	}
}

// -----------------------------------------------------------------------------
// Dial（真实 TCP + SSH 握手）
// -----------------------------------------------------------------------------

func (p *SessionPool) dial(ctx context.Context, dt *dialTarget) (*ssh.Client, error) {
	cfg, err := buildClientConfig(dt)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = p.opts.DialTimeout

	addr := net.JoinHostPort(dt.Host, fmt.Sprintf("%d", dt.Port))
	dial := p.opts.Dialer
	if dial == nil {
		dial = ssh.Dial
	}

	type result struct {
		c   *ssh.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := dial("tcp", addr, cfg)
		ch <- result{c, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("ssh dial %s: %w", addr, r.err)
		}
		return r.c, nil
	}
}

// buildClientConfig 根据凭据构建 *ssh.ClientConfig。
func buildClientConfig(dt *dialTarget) (*ssh.ClientConfig, error) {
	if dt.User == "" {
		return nil, errors.New("ssh: user is empty")
	}
	cfg := &ssh.ClientConfig{
		User: dt.User,
	}

	auth, err := buildAuthMethods(&dt.Creds)
	if err != nil {
		return nil, err
	}
	if len(auth) == 0 {
		return nil, errors.New("ssh: no auth method resolved from credentials")
	}
	cfg.Auth = auth

	cb, err := buildHostKeyCallback(&dt.Creds)
	if err != nil {
		return nil, err
	}
	cfg.HostKeyCallback = cb
	return cfg, nil
}

func buildAuthMethods(c *sshCredentials) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	switch c.Method {
	case "password":
		if c.Password == "" {
			return nil, errors.New("ssh: password is empty")
		}
		methods = append(methods, ssh.Password(c.Password))

	case "privatekey":
		if c.PrivateKey == "" {
			return nil, errors.New("ssh: private_key is empty")
		}
		var (
			signer ssh.Signer
			err    error
		)
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("ssh: parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))

	case "agent":
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, errors.New("ssh: SSH_AUTH_SOCK not set for agent auth")
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, fmt.Errorf("ssh: dial agent: %w", err)
		}
		methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))

	default:
		return nil, fmt.Errorf("ssh: unknown auth method %q", c.Method)
	}
	return methods, nil
}

func buildHostKeyCallback(c *sshCredentials) (ssh.HostKeyCallback, error) {
	mode := c.KnownHostsMode
	if mode == "" {
		mode = "insecure-ignore" // v0.1 缺省，后续切换到 strict
	}
	switch mode {
	case "insecure-ignore":
		return ssh.InsecureIgnoreHostKey(), nil
	case "strict", "accept-new":
		if c.KnownHostsPath == "" {
			return nil, errors.New("ssh: known_hosts_path required for strict mode")
		}
		cb, err := knownhosts.New(c.KnownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("ssh: load known_hosts: %w", err)
		}
		return cb, nil
	default:
		return nil, fmt.Errorf("ssh: unknown known_hosts_mode %q", mode)
	}
}
