//go:build linux

package termutil

// Terminal ioctl request codes for Linux.
const (
	ioctlReadTermios  = 0x5401 // TCGETS
	ioctlWriteTermios = 0x5402 // TCSETS
)
