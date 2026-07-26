package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

type directoryWalkFunc func(string, filepath.WalkFunc) error

// MeasureDirectorySize returns the approximate size of the live file tree at
// root. It tolerates descendants disappearing while the tree is being walked,
// but a missing root or any other filesystem error is a failed measurement.
func MeasureDirectorySize(ctx context.Context, root string) (int64, error) {
	if root == "" {
		return 0, fmt.Errorf("directory path is empty")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", root)
	}

	return measureDirectorySizeWithWalk(ctx, resolvedRoot, filepath.Walk)
}

func measureDirectorySizeWithWalk(ctx context.Context, root string, walk directoryWalkFunc) (int64, error) {
	var size int64
	err := walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path != root && errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if info == nil {
			return fmt.Errorf("walk returned no file information for %s", path)
		}
		if info.IsDir() {
			return nil
		}
		if info.Size() < 0 || info.Size() > math.MaxInt64-size {
			return fmt.Errorf("directory size overflows int64 at %s", path)
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return size, nil
}
