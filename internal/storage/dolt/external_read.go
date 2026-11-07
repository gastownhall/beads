package dolt

import (
	"context"
	"fmt"
	"path/filepath"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// NewExternalReadOnly opens a strictly non-mutating read-only connection to
// the external dolt sql-server a foreign proxied-server workspace's sidecar
// names (#5861 item 2): beadsDir is that workspace's .beads, database its
// literal persisted dolt_database, external the validated sidecar endpoint
// description, and password the explicitly supplied external password (empty
// is authoritative — the caller resolves it from
// BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD and this constructor never falls
// back to BEADS_DOLT_PASSWORD or any ambient setting).
//
// The open reuses the ordinary server-mode machinery (newServerMode,
// openServerConnection, the schema-drift and project-identity checks) under
// the Config.ExternalReadOnly posture: ReadOnly, no creation, no auto-start,
// no ambient endpoint resolution, and no local storage ownership, including
// for an external server on loopback. See that field's comment for the full
// contract. A database that does not exist fails with the ordinary
// database-not-found error and creates nothing.
func NewExternalReadOnly(ctx context.Context, beadsDir, database string, external configfile.ExternalDoltConfig, password string) (*DoltStore, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("external read: target workspace (.beads) is required")
	}
	if database == "" {
		return nil, fmt.Errorf("external read: database name must not be empty (the target metadata names no dolt_database)")
	}
	if err := external.Validate(); err != nil {
		return nil, fmt.Errorf("external read: %w", err)
	}
	cfg := externalReadOnlyConfig(beadsDir, database, external, password)
	if external.TLSRequired {
		name, err := registerExternalReadOnlyTLSConfig(external)
		if err != nil {
			return nil, fmt.Errorf("external read: %w", err)
		}
		cfg.ServerTLSConfigName = name
	}
	return New(ctx, cfg)
}

// externalReadOnlyConfig builds the resolved external-read Config. Every
// connection field is set from the caller's values and nothing else; an empty
// password or an absent host stays that way (applyExternalReadOnlyDefaults
// adds no ambient fallback).
func externalReadOnlyConfig(beadsDir, database string, external configfile.ExternalDoltConfig, password string) *Config {
	return &Config{
		// Path is the workspace's own dolt directory, for error messages and
		// the BeadsDir fallback only: this posture never opens it (no
		// embedded engine, no proxy, no server start), and
		// resolveLocalActiveDatabaseDir refuses it outright.
		Path:                  filepath.Join(beadsDir, "dolt"),
		BeadsDir:              beadsDir,
		Database:              database,
		ReadOnly:              true,
		RequireReadableSchema: true,
		CreateIfMissing:       false,
		DisableAutoStart:      true,
		ExternalReadOnly:      true,
		ServerSocket:          external.Socket,
		ServerHost:            external.Host,
		ServerPort:            external.Port,
		ServerUser:            external.ResolvedUser(),
		ServerPassword:        password,
		ServerTLS:             external.TLSRequired,
	}
}

// registerExternalReadOnlyTLSConfig puts the sidecar's full TLS configuration
// on the MySQL connection by name. Registering a name alone would be
// insufficient — buildServerDSN must also place it on the DSN — and the
// boolean ServerTLS field cannot carry a CA, client certificate or server
// name. The name mirrors the external provider's registration
// (uow.registerExternalTLSConfig): re-registering the same name overwrites
// the entry in place, so repeated opens stay safe.
func registerExternalReadOnlyTLSConfig(external configfile.ExternalDoltConfig) (string, error) {
	if !external.TLSRequired {
		return "", nil
	}
	tc, err := external.TLSClientConfig()
	if err != nil {
		return "", err
	}
	name := "beads-external-" + server.ExternalDoltServerID(external)
	if err := mysql.RegisterTLSConfig(name, tc); err != nil {
		return "", fmt.Errorf("register TLS config: %w", err)
	}
	return name, nil
}
