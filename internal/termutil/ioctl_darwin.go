//go:build darwin

package termutil

// Terminal ioctl request codes for macOS/Darwin.
const (
	ioctlReadTermios  = 0x40487413 // TIOCGETA
	ioctlWriteTermios = 0x80487414 // TIOCSETA
)
