package connection

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// -----------------------------------------------------------------------------
// KeyStore（主密钥）
// -----------------------------------------------------------------------------
//
// v0.1：主密钥（32 字节）以 base64 形态落盘于 <DataDir>/keys/master.key，
// 权限 0600。若不存在则自动生成。
//
// v0.2 计划：
//   - 优先使用 zalando/go-keyring 存到平台密钥库
//   - 桌面环境不可用时回退到"主口令 + PBKDF2 派生密钥"
//
// 这里只保留最小可用实现，接口收敛在 KeyStore，之后替换实现即可。

// KeyStore 抽象了主密钥的读写。
type KeyStore interface {
	// Key 返回 32 字节的 AES-256 主密钥。
	Key() ([]byte, error)
}

// FileKeyStore 把主密钥以 base64 形式存到本地文件。
type FileKeyStore struct {
	Path string
	mu   sync.Mutex
	key  []byte
}

// NewFileKeyStore 构造一个 FileKeyStore。
// path 一般为 <DataDir>/keys/master.key。
func NewFileKeyStore(path string) *FileKeyStore {
	return &FileKeyStore{Path: path}
}

// Key 返回主密钥；不存在时自动生成并落盘。
func (s *FileKeyStore) Key() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.key) == 32 {
		return s.key, nil
	}
	if s.Path == "" {
		return nil, errors.New("connection: key store path is empty")
	}

	data, err := os.ReadFile(s.Path)
	if err == nil {
		key, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return nil, fmt.Errorf("connection: decode master key: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("connection: master key size = %d, want 32", len(key))
		}
		s.key = key
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("connection: read master key: %w", err)
	}

	// 生成新的主密钥
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("connection: generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return nil, fmt.Errorf("connection: mkdir key dir: %w", err)
	}
	enc := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(s.Path, []byte(enc), 0o600); err != nil {
		return nil, fmt.Errorf("connection: write master key: %w", err)
	}
	s.key = key
	return key, nil
}

// StaticKeyStore 直接持有明文密钥，供测试与嵌入式场景使用。
type StaticKeyStore struct{ K []byte }

func (s StaticKeyStore) Key() ([]byte, error) {
	if len(s.K) != 32 {
		return nil, fmt.Errorf("connection: static key size = %d, want 32", len(s.K))
	}
	return s.K, nil
}

// -----------------------------------------------------------------------------
// Sealer（AES-256-GCM）
// -----------------------------------------------------------------------------

// Sealer 基于 KeyStore 提供加解密能力。
// 输出格式：nonce(12) || ciphertext || tag(16)
type Sealer struct {
	ks KeyStore
}

// NewSealer 构造一个 Sealer。
func NewSealer(ks KeyStore) *Sealer { return &Sealer{ks: ks} }

// Seal 加密明文字节。
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("connection: nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open 解密密文字节。
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, nil
	}
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(sealed) < ns+gcm.Overhead() {
		return nil, errors.New("connection: sealed payload too short")
	}
	nonce := sealed[:ns]
	ct := sealed[ns:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("connection: decrypt: %w", err)
	}
	return pt, nil
}

func (s *Sealer) gcm() (cipher.AEAD, error) {
	key, err := s.ks.Key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("connection: aes: %w", err)
	}
	return cipher.NewGCM(block)
}
