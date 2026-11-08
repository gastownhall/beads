package dolt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
)

// ExternalReadConfig is the endpoint NewExternalReadOnly reads: another
// workspace's external Dolt server, as that workspace's proxied-server
// sidecar (configfile.ProxiedServerClientInfo.External) describes it.
type ExternalReadConfig struct {
	// BeadsDir is the target workspace's .beads directory. The open checks
	// the database's project identity and the endpoint's trust pins against
	// it, and writes nothing there.
	BeadsDir string
	// Database is the target's dolt_database as its metadata.json names it.
	Database string
	// External is the sidecar's endpoint: a socket or host and port, the
	// user, and the TLS settings.
	External configfile.ExternalDoltConfig
	// Password is presented exactly as given, and empty means none. The
	// caller supplies it (configfile.ExternalDoltPasswordEnvVar); it is never
	// filled from BEADS_DOLT_PASSWORD.
	Password string //nolint:gosec // G117: the connection's password, not serialized
}

// NewExternalReadOnly opens a read-only store on an external Dolt server for
// a workspace bd was not launched in, without that workspace's proxy
// (gastownhall/beads#5861). It is the strict non-mutating posture
// (Config.ExternalRead): it starts no proxy or server, creates no database,
// runs no migration, and writes no file in the target workspace, the
// database missing included. The endpoint, user and password are rc's alone;
// nothing of the launcher's connection settings fills them in. A password is
// presented only to a socket the user owns, bd's own loopback server, or an
// endpoint already pinned for the target workspace
// (doltserver.CheckExternalReadTrust, which New runs), never on the strength
// of the launcher's own host settings, and every TLS setting the sidecar
// names reaches the connection.
func NewExternalReadOnly(ctx context.Context, rc ExternalReadConfig) (*DoltStore, error) {
	if rc.BeadsDir == "" {
		return nil, errors.New("external read: target workspace directory is required")
	}
	if rc.Database == "" {
		return nil, fmt.Errorf("external read of %s: the workspace names no dolt_database", remedy.PrintedValue(rc.BeadsDir))
	}
	if err := rc.External.Validate(); err != nil {
		return nil, fmt.Errorf("external read of %s: %w", remedy.PrintedValue(rc.BeadsDir), err)
	}
	tlsName, err := registerExternalReadTLS(rc.External)
	if err != nil {
		return nil, fmt.Errorf("external read of %s: %w", remedy.PrintedValue(rc.BeadsDir), err)
	}
	cfg := &Config{
		// New requires a path; nothing opens it (localActiveDatabaseDir
		// stays empty for an external read).
		Path:                  filepath.Join(rc.BeadsDir, "dolt"),
		BeadsDir:              rc.BeadsDir,
		Database:              rc.Database,
		ReadOnly:              true,
		RequireReadableSchema: true,
		DisableAutoStart:      true,
		ServerMode:            true,
		ExternalRead:          true,
		ServerSocket:          rc.External.Socket,
		ServerHost:            rc.External.Host,
		ServerPort:            rc.External.Port,
		ServerUser:            rc.External.ResolvedUser(),
		ServerPassword:        rc.Password,
		ServerTLS:             rc.External.TLSRequired,
		ServerTLSConfigName:   tlsName,
	}
	if cfg.ServerPort != 0 {
		cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
	}
	return New(ctx, cfg)
}

// registerExternalReadTLS registers external's TLS settings with the MySQL
// driver under a name derived from them and returns it, or "" when external
// does not require TLS.
func registerExternalReadTLS(external configfile.ExternalDoltConfig) (string, error) {
	if !external.TLSRequired {
		return "", nil
	}
	tc, err := external.TLSClientConfig()
	if err != nil {
		return "", err
	}
	key, err := json.Marshal(external)
	if err != nil {
		return "", fmt.Errorf("naming TLS config: %w", err)
	}
	sum := sha256.Sum256(key)
	name := "beads-external-read-" + hex.EncodeToString(sum[:8])
	if err := mysql.RegisterTLSConfig(name, tc); err != nil {
		return "", fmt.Errorf("register TLS config: %w", err)
	}
	return name, nil
}
