package main

import "os"

// isTerminal 判断给定 fd 是否为字符设备（TTY）。
// 只做粗判：ModeCharDevice 存在即视为 TTY。
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}
