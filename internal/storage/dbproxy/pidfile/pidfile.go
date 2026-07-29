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
	Pid         int    `json:"pid"`
	Port        int    `json:"port"`
	UpstreamID  string `json:"upstream_id,omitempty"`
	Schema      int    `json:"schema,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Birth       string `json:"birth,omitempty"`
	RootID      string `json:"root_id,omitempty"`
	ControlPort int    `json:"control_port,omitempty"`
}

const SchemaV2 = 2

const (
	KindProxy       = "db-proxy"
	KindDoltBackend = "dolt-backend"
)

var (
	ErrLegacySchema = errors.New("pidfile: legacy schema")
	ErrBadPid       = errors.New("pidfile: invalid pid")
	ErrBadPort      = errors.New("pidfile: invalid port")
	ErrKindMismatch = errors.New("pidfile: kind mismatch")
	ErrMissingBirth = errors.New("pidfile: missing birth token")
)

// ValidateV2 validates the fields required for a schema v2 pidfile.
func (p *PidFile) ValidateV2(wantKind string) error {
	if p.Schema < SchemaV2 {
		return ErrLegacySchema
	}
	if p.Pid <= 0 {
		return ErrBadPid
	}
	if p.Port < 1 || p.Port > 65535 || (p.ControlPort != 0 && (p.ControlPort < 1 || p.ControlPort > 65535)) {
		return ErrBadPort
	}
	if p.Kind != wantKind {
		return ErrKindMismatch
	}
	if p.Birth == "" {
		return ErrMissingBirth
	}
	return nil
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
