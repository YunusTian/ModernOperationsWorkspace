//go:build unix

package history

// flock_unix.go —— 跨进程文件锁的 Unix 平台实现。
//
// 语义：
//   - lockFile(f) 对整个文件加独占锁；同一进程内多次调用同一 fd 会阻塞
//     （由内核 flock 行为决定）。测试要求"同一进程内 sync.Mutex 已序列化 Save"，
//     所以这里只解决跨进程 append 交叉行的问题。
//   - unlockFile(f) 显式释放；进程崩溃时内核也会自动释放，属于免死锁的
//     advisory lock，最坏情况下另一进程会短暂阻塞。
//
// 依赖 golang.org/x/sys/unix，原本已被 hashicorp/go-plugin 的间接传入过，
// 现在升为 direct dependency。
//
// 为什么用 flock 而不是 fcntl(F_SETLK)：
//   - flock 语义更简单：per-open-file-description，与 os.File 一一对应
//   - Linux/macOS/BSD 都原生支持
//   - fcntl 的 close-any-fd-drops-all-locks 陷阱不适合我们这种"打开-写-关闭"的模式

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) error {
	// LOCK_EX：独占；不带 LOCK_NB → 阻塞直到拿到锁。
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
