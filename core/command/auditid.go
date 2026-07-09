package command

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
)

// NewAuditID 生成一个全局唯一的审计 ID。
//
// 格式：<unix-ms-16hex>-<seq-4hex>-<rand-12hex>
// 例：018f4a3c1e2b0000-0001-9a7f4c2b0d1e
//
// 设计目标：
//   - 时间前缀：便于按时间排序
//   - 序号：同毫秒内的原子递增，避免碰撞
//   - 随机后缀：防止跨进程碰撞
//   - 无外部依赖（不引入 ULID / UUID 库）
//
// 未来引入 UUIDv7 / ULID 时保持函数签名不变。
func NewAuditID() string {
	ms := time.Now().UnixMilli()
	seq := atomic.AddUint32(&auditSeq, 1) & 0xFFFF

	var rnd [6]byte
	_, _ = rand.Read(rnd[:]) // 失败时以零填充，仍可用

	// unix-ms 用 16 位 hex 足以覆盖到公元 5000+
	buf := make([]byte, 0, 16+1+4+1+12)
	buf = append(buf, padHex(uint64(ms), 16)...)
	buf = append(buf, '-')
	buf = append(buf, padHex(uint64(seq), 4)...)
	buf = append(buf, '-')
	buf = append(buf, hex.EncodeToString(rnd[:])...)
	return string(buf)
}

var auditSeq uint32

func padHex(v uint64, width int) string {
	s := strconv.FormatUint(v, 16)
	if len(s) >= width {
		return s[len(s)-width:]
	}
	pad := make([]byte, width-len(s))
	for i := range pad {
		pad[i] = '0'
	}
	return string(pad) + s
}
