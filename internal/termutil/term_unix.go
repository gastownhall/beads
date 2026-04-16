//go:build unix

package termutil

import (
	"syscall"
	"unsafe"
)

// ioctlReadTermios is the platform-specific ioctl request code for reading
// the terminal attributes (termios struct). Defined per-platform in
// ioctl_linux.go / ioctl_darwin.go.

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

// IsTerminal reports whether fd refers to a terminal.
func IsTerminal(fd int) bool {
	var t syscall.Termios
	//nolint:gosec // G103: required for ioctl
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlReadTermios, uintptr(unsafe.Pointer(&t)))
	return err == 0
}

// GetSize returns the visible dimensions of the terminal referred to by fd.
func GetSize(fd int) (width, height int, err error) {
	var ws winsize
	//nolint:gosec // G103: required for ioctl
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0, 0, errno
	}
	return int(ws.Col), int(ws.Row), nil
}

// ReadPassword reads a line of input from fd without echoing it.
// The returned slice does not include the trailing newline.
func ReadPassword(fd int) ([]byte, error) {
	var old syscall.Termios
	//nolint:gosec // G103: required for ioctl
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlReadTermios, uintptr(unsafe.Pointer(&old))); err != 0 {
		return nil, err
	}

	noecho := old
	noecho.Lflag &^= syscall.ECHO | syscall.ECHONL
	noecho.Lflag |= syscall.ICANON
	//nolint:gosec // G103: required for ioctl
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlWriteTermios, uintptr(unsafe.Pointer(&noecho))); err != 0 {
		return nil, err
	}
	//nolint:gosec // G103: required for ioctl
	defer syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlWriteTermios, uintptr(unsafe.Pointer(&old))) //nolint:errcheck

	var buf [512]byte
	n := 0
	for {
		nn, err := syscall.Read(fd, buf[n:n+1])
		if err != nil {
			return buf[:n], err
		}
		if nn == 0 || buf[n] == '\n' || buf[n] == '\r' {
			break
		}
		n++
		if n >= len(buf)-1 {
			break
		}
	}
	return buf[:n], nil
}
