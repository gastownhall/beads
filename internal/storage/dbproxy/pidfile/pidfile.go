package pidfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/atomicfile"
)

type PidFile struct {
	Pid        int    `json:"pid"`
	Port       int    `json:"port"`
	UpstreamID string `json:"upstream_id,omitempty"`
}

func Path(rootDir, name string) string {
	return filepath.Join(rootDir, name)
}

func Read(rootDir, name string) (*PidFile, error) {
	data, err := os.ReadFile(Path(rootDir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var pf PidFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}

func Write(rootDir, name string, pf PidFile) error {
	data, err := json.Marshal(pf)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(Path(rootDir, name), data, 0o644)
}

func Remove(rootDir, name string) error {
	err := os.Remove(Path(rootDir, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
