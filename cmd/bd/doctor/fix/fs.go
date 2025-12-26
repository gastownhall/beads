package fix

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

var (
	renameFile = os.Rename
	removeFile = os.Remove
	openFileRO = os.Open
	openFileRW = os.OpenFile
)

func moveFile(src, dst string) error {
	if err := renameFile(src, dst); err == nil {
		return nil
	} else if isEXDEV(err) {
		if err := copyFile(src, dst); err != nil {
			return err
		}
		if err := removeFile(src); err != nil {
			return fmt.Errorf("failed to remove source after copy: %w", err)
		}
		return nil
	} else {
		return err
	}
}

func copyFile(src, dst string) error {
	in, err := openFileRO(src) // #nosec G304 -- src is within the workspace
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := openFileRW(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func isEXDEV(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}
