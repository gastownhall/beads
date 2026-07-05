//go:build !windows && !linux

package doltserver

import "syscall"

func applyTestModeParentDeathSignal(_ *syscall.SysProcAttr) {}
