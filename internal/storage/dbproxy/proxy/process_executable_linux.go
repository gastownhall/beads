//go:build linux

package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

func processExecutableBasename(pid int) (basename string, gone bool, err error) {
	target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", true, nil
		}
		return "", false, err
	}
	base := filepath.Base(target)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", false, fmt.Errorf("empty executable basename for pid %d", pid)
	}
	return base, false, nil
}
