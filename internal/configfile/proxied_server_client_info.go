package configfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const ProxiedServerClientInfoFileName = "proxied_server_client_info.json"

type ProxiedServerClientInfo struct {
	RootPath    string              `json:"root_path,omitempty"`
	ConfigPath  string              `json:"config_path,omitempty"`
	LogPath     string              `json:"log_path,omitempty"`
	Port        int                 `json:"port,omitempty"`
	IdleTimeout time.Duration       `json:"idle_timeout,omitempty"`
	External    *ExternalDoltConfig `json:"external,omitempty"`
}

func ProxiedServerClientInfoPath(beadsDir string) string {
	return filepath.Join(beadsDir, ProxiedServerClientInfoFileName)
}

func LoadProxiedServerClientInfo(beadsDir string) (*ProxiedServerClientInfo, error) {
	path := ProxiedServerClientInfoPath(beadsDir)
	data, err := os.ReadFile(path) // #nosec G304 - controlled path
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ProxiedServerClientInfoFileName, err)
	}
	var info ProxiedServerClientInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ProxiedServerClientInfoFileName, err)
	}
	return &info, nil
}

func SaveProxiedServerClientInfo(beadsDir string, info *ProxiedServerClientInfo) error {
	if info == nil {
		info = &ProxiedServerClientInfo{}
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", ProxiedServerClientInfoFileName, err)
	}
	path := ProxiedServerClientInfoPath(beadsDir)
	tmp, err := os.CreateTemp(beadsDir, ".proxied-server-client-info-*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", ProxiedServerClientInfoFileName, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", ProxiedServerClientInfoFileName, err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("writing %s: %w", ProxiedServerClientInfoFileName, err)
	}
	d, err := os.Open(beadsDir) // #nosec G304 -- beadsDir is the discovered workspace directory
	if err != nil {
		return fmt.Errorf("syncing %s directory: %w", ProxiedServerClientInfoFileName, err)
	}
	if err = d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("syncing %s directory: %w", ProxiedServerClientInfoFileName, err)
	}
	if err = d.Close(); err != nil {
		return fmt.Errorf("closing %s directory: %w", ProxiedServerClientInfoFileName, err)
	}
	return nil
}

func resolveSidecarPath(beadsDir, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(beadsDir, p)
}

func (i *ProxiedServerClientInfo) ResolvedRootPath(beadsDir string) string {
	if i == nil {
		return ""
	}
	return resolveSidecarPath(beadsDir, i.RootPath)
}

func (i *ProxiedServerClientInfo) ResolvedConfigPath(beadsDir string) string {
	if i == nil {
		return ""
	}
	return resolveSidecarPath(beadsDir, i.ConfigPath)
}

func (i *ProxiedServerClientInfo) ResolvedLogPath(beadsDir string) string {
	if i == nil {
		return ""
	}
	return resolveSidecarPath(beadsDir, i.LogPath)
}
