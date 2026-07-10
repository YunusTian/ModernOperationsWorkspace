//go:build windows

package history

// flock_windows.go —— 跨进程文件锁的 Windows 平台实现。
//
// 语义：
//   - lockFile(f) 用 LockFileEx 对整个文件区间加独占锁；同一 handle 上不能
//     被同一进程重复加同一区间的独占锁（Windows 行为），因此调用方需要保证
//     同一时刻同一 handle 只调一次。JSONLStore.Save 打开新 fd 就锁，
//     Close 前 unlock，天然满足。
//   - unlockFile(f) 释放；handle 关闭时 Windows 也会自动释放，避免死锁。
//
// 依赖 golang.org/x/sys/windows。

import (
	"os"

	"golang.org/x/sys/windows"
)

// 独占 + 阻塞：不传 LOCKFILE_FAIL_IMMEDIATELY，让 LockFileEx 等待锁。
const lockFlagsExclusive = windows.LOCKFILE_EXCLUSIVE_LOCK

// lockRegion 锁整个文件（[0, MaxUint32) 高低两半的最大区间）。
// JSONL 单文件不会超过 4GiB，超过即被 rotate 切成新文件，因此单进程内足够。
var (
	regionLo uint32 = 0xFFFFFFFF
	regionHi uint32 = 0xFFFFFFFF
)

func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), lockFlagsExclusive, 0, regionLo, regionHi, ol)
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, regionLo, regionHi, ol)
}
