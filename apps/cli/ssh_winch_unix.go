//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func signalWinchSupported() bool { return true }

func sigWinch() os.Signal { return unix.SIGWINCH }
