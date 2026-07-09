//go:build windows

package main

import "os"

func signalWinchSupported() bool { return false }

// sigWinch 在 Windows 上无实际作用，仅为满足编译。
func sigWinch() os.Signal { return nil }
