//go:build darwin

package proxy

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func processExecutableBasename(pid int) (basename string, gone bool, err error) {
	output, commandErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if commandErr != nil {
		if killErr := syscall.Kill(pid, 0); errors.Is(killErr, unix.ESRCH) {
			return "", true, nil
		}
		return "", false, commandErr
	}
	base := filepath.Base(strings.TrimSpace(string(output)))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", false, fmt.Errorf("empty executable basename for pid %d", pid)
	}
	return base, false, nil
}
