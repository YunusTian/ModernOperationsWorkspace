package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

	refs     int
	lastUsed time.Time
	closed   bool
}

type pendingDial struct {
	done chan struct{}
	err  error
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
	dialing map[string]*pendingDial

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
		dialing: map[string]*pendingDial{},
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
	if pending, ok := p.dialing[key]; ok {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, key, ctx.Err()
		case <-pending.done:
		}

		p.mu.Lock()
		defer p.mu.Unlock()
		if pending.err != nil {
			return nil, key, pending.err
		}
		pc, ok := p.clients[key]
		if !ok || pc.closed {
			return nil, key, errors.New("ssh: pooled client unavailable after dial")
		}
		pc.refs++
		pc.lastUsed = time.Now()
		return pc.Client, key, nil
	}
	pending := &pendingDial{done: make(chan struct{})}
	p.dialing[key] = pending
	p.mu.Unlock()

	client, err := p.dial(ctx, dt)
	if err != nil {
		p.mu.Lock()
		delete(p.dialing, key)
		pending.err = err
		close(pending.done)
		p.mu.Unlock()
		return nil, key, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.dialing, key)
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
	close(pending.done)
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
		// #nosec G704 -- the local agent socket path is an explicit user/runtime
		// selection and net.Dial is intentionally limited to the unix network.
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
		mode = "strict" // 安全默认：必须命中 known_hosts
	}
	switch mode {
	case "insecure-ignore":
		// 显式选择的 opt-in 模式；供受控测试环境或用户明确豁免使用。
		// gosec 会报 G106，但这里的语义是"用户显式承担该风险"。
		return ssh.InsecureIgnoreHostKey(), nil // #nosec G106 -- explicit opt-in insecure mode

	case "strict":
		if c.KnownHostsPath == "" {
			return nil, errors.New("ssh: known_hosts_path required for strict mode")
		}
		cb, err := knownhosts.New(c.KnownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("ssh: load known_hosts: %w", err)
		}
		return cb, nil

	case "accept-new":
		if c.KnownHostsPath == "" {
			return nil, errors.New("ssh: known_hosts_path required for accept-new mode")
		}
		return newAcceptNewCallback(c.KnownHostsPath)

	default:
		return nil, fmt.Errorf("ssh: unknown known_hosts_mode %q", mode)
	}
}

// newAcceptNewCallback 返回一个 HostKeyCallback：
//   - 若主机键已存在于 known_hosts，则按 strict 模式校验
//   - 若主机键为"未知主机"，则把该条追加到 known_hosts 并放行
//   - 若主机键与已记录键"不匹配"（可能的 MITM），则拒绝
//
// 语义对齐 OpenSSH 的 StrictHostKeyChecking=accept-new。
func newAcceptNewCallback(path string) (ssh.HostKeyCallback, error) {
	// 若文件不存在，先创建空文件，避免 knownhosts.New 失败。
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("ssh: mkdir known_hosts: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("ssh: create known_hosts: %w", err)
		}
		_ = f.Close()
	}

	strict, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: load known_hosts: %w", err)
	}

	var mu sync.Mutex
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := strict(hostname, remote, key); err == nil {
			return nil
		} else if !isUnknownHostKey(err) {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		return appendKnownHost(path, hostname, remote, key)
	}, nil
}

// isUnknownHostKey 判定 knownhosts 报错是否"未知主机"而非"密钥不匹配"。
// x/crypto/ssh/knownhosts 用 *KeyError 表示两类错误：Want 为空 → 未知主机。
func isUnknownHostKey(err error) bool {
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return false
	}
	return len(ke.Want) == 0
}

// appendKnownHost 把 (hostname, remote, key) 追加到 known_hosts。
func appendKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("ssh: open known_hosts: %w", err)
	}
	defer f.Close()

	addresses := []string{hostname}
	if remote != nil && remote.String() != hostname {
		addresses = append(addresses, remote.String())
	}
	line := knownhosts.Line(addresses, key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("ssh: append known_hosts: %w", err)
	}
	return nil
}
