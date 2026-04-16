//go:build windows

package termutil

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

type consoleScreenBufferInfo struct {
	Size       [2]int16
	CursorPos  [2]int16
	Attributes uint16
	Window     [4]int16 // left, top, right, bottom
	MaxWinSize [2]int16
}

// IsTerminal reports whether fd refers to a terminal (console).
func IsTerminal(fd int) bool {
	var mode uint32
	//nolint:gosec // G103: required for Windows API call
	r, _, _ := procGetConsoleMode.Call(uintptr(fd), uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

// GetSize returns the visible dimensions of the console referred to by fd.
func GetSize(fd int) (width, height int, err error) {
	var info consoleScreenBufferInfo
	//nolint:gosec // G103: required for Windows API call
	r, _, e := procGetConsoleScreenBufferInfo.Call(uintptr(fd), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, 0, e
	}
	return int(info.Window[2] - info.Window[0] + 1), int(info.Window[3] - info.Window[1] + 1), nil
}

// ReadPassword reads a line of input from fd without echoing it.
// The returned slice does not include the trailing newline.
func ReadPassword(fd int) ([]byte, error) {
	var oldMode uint32
	//nolint:gosec // G103: required for Windows API call
	r, _, e := procGetConsoleMode.Call(uintptr(fd), uintptr(unsafe.Pointer(&oldMode)))
	if r == 0 {
		return nil, e
	}

	const enableEchoInput = 0x4
	newMode := oldMode &^ enableEchoInput
	//nolint:gosec // G103: required for Windows API call
	r, _, e = procSetConsoleMode.Call(uintptr(fd), uintptr(newMode))
	if r == 0 {
		return nil, e
	}
	defer procSetConsoleMode.Call(uintptr(fd), uintptr(oldMode)) //nolint:errcheck

	var buf [512]byte
	var oneByte [1]byte
	n := 0
	for n < len(buf)-1 {
		var bytesRead uint32
		err := syscall.ReadFile(syscall.Handle(fd), oneByte[:], &bytesRead, nil)
		if err != nil || bytesRead == 0 {
			break
		}
		if oneByte[0] == '\r' || oneByte[0] == '\n' {
			break
		}
		buf[n] = oneByte[0]
		n++
	}
	return buf[:n], nil
}
