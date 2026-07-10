package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// -----------------------------------------------------------------------------
// Docker multiplexed stream 解码
// -----------------------------------------------------------------------------
//
// 当 tty=false 时（默认），docker daemon 会把 stdout / stderr 复用到同一 HTTP
// body 中，帧头是 8 字节：
//
//     header := [8]byte{STREAM_TYPE, 0, 0, 0, SIZE1, SIZE2, SIZE3, SIZE4}
//     STREAM_TYPE ∈ {0=stdin,1=stdout,2=stderr}
//     SIZE 是 big-endian uint32，表示紧跟在头之后的 payload 字节数
//
// 当 tty=true 时，body 就是原始字节流，直接透传即可。
//
// 参考：https://docs.docker.com/engine/api/v1.44/#tag/Container/operation/ContainerAttach

// stdType 是复用流中的通道类型。
type stdType byte

const (
	stdStdin  stdType = 0
	stdStdout stdType = 1
	stdStderr stdType = 2
)

// dockerHeaderSize 是复用流每帧头的固定字节数。
const dockerHeaderSize = 8

// muxReader 从 Docker 复用流中依次读取帧。
// 用法：
//
//	mr := newMuxReader(body)
//	for {
//	    kind, chunk, err := mr.NextFrame()
//	    if err == io.EOF { break }
//	    if err != nil { return err }
//	    switch kind { case stdStdout: ... }
//	}
type muxReader struct {
	r      io.Reader
	header [dockerHeaderSize]byte
	buf    []byte
}

func newMuxReader(r io.Reader) *muxReader {
	return &muxReader{r: r, buf: make([]byte, 32*1024)}
}

// nextFrame 读取一帧。
//
//	kind  ∈ stdStdout / stdStderr / stdStdin
//	chunk 是本帧 payload；下一次调用会复用底层缓冲，调用方需自行拷贝
//
// io.EOF 表示流正常结束。
func (m *muxReader) nextFrame() (stdType, []byte, error) {
	if _, err := io.ReadFull(m.r, m.header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	kind := stdType(m.header[0])
	// 只接受 0/1/2；其它值代表 tty=true 场景下我们错误地按复用格式解析——上层应先判断。
	if kind > stdStderr {
		return 0, nil, fmt.Errorf("mux: unknown stream type %d (tty mismatch?)", kind)
	}
	size := binary.BigEndian.Uint32(m.header[4:8])
	if size == 0 {
		return kind, nil, nil
	}
	if int(size) > cap(m.buf) {
		m.buf = make([]byte, size)
	} else {
		m.buf = m.buf[:size]
	}
	if _, err := io.ReadFull(m.r, m.buf); err != nil {
		return 0, nil, err
	}
	return kind, m.buf, nil
}
