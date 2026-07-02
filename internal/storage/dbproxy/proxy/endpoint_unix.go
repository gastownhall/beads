//go:build unix

package proxy

import "syscall"

func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
