package ownershiphandoff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const gcHandoffSchemaVersion = 1

// GCProvider invokes the explicit hidden Gas City handoff protocol. The
// binary path must be absolute, canonical, and executable; no PATH lookup or
// process-state fallback is permitted.
type GCProvider struct {
	Binary string
}

// NewGCProvider validates a trusted absolute GC executable path.
func NewGCProvider(binary string) (Provider, error) {
	if err := validateGCExecutable(binary); err != nil {
		return nil, err
	}
	return &GCProvider{Binary: binary}, nil
}

func validateGCExecutable(binary string) error {
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		return CodedError{Code: "provider_unavailable", Err: errors.New("GC_BIN must be an absolute canonical path")}
	}
	info, err := os.Lstat(binary)
	if err != nil {
		return CodedError{Code: "provider_unavailable", Err: fmt.Errorf("stat GC_BIN: %w", err)}
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return CodedError{Code: "provider_unavailable", Err: errors.New("GC_BIN must be an executable regular file")}
	}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil || real != binary {
		if err == nil {
			err = errors.New("GC_BIN must not be symlinked")
		}
		return CodedError{Code: "provider_unavailable", Err: err}
	}
	return nil
}

// NewGCProviderFromEnv creates a provider using the trusted GC_BIN
// environment variable. An unset variable returns an unavailable provider
// rather than searching PATH.
func NewGCProviderFromEnv() Provider {
	return ProviderFunc(func(ctx context.Context, r Request) (Hooks, error) {
		binary := os.Getenv("GC_BIN")
		if binary == "" {
			return Hooks{}, CodedError{Code: "provider_unavailable", Err: errors.New("GC_BIN is not configured")}
		}
		provider, err := NewGCProvider(binary)
		if err != nil {
			return Hooks{}, err
		}
		return provider.OwnershipHandoffHooks(ctx, r)
	})
}

type gcHandoffIdentity struct {
	CityRoot       string   `json:"city_root"`
	ScopeRoot      string   `json:"scope_root"`
	Database       string   `json:"database"`
	Workspace      string   `json:"workspace"`
	Endpoint       Endpoint `json:"endpoint"`
	DataDir        string   `json:"data_dir"`
	ConfigFile     string   `json:"config_file"`
	PID            int      `json:"pid"`
	StartIdentity  string   `json:"start_identity"`
	StartTimeTicks int64    `json:"start_time_ticks"`
	PortHolderPID  int      `json:"port_holder_pid"`
}

type gcHandoffResponse struct {
	SchemaVersion int               `json:"schema_version"`
	Operation     string            `json:"operation"`
	Result        string            `json:"result"`
	Owner         Owner             `json:"owner"`
	Mutates       bool              `json:"mutates"`
	Identity      gcHandoffIdentity `json:"identity"`
	IdentityToken string            `json:"identity_token"`
	ErrorCode     string            `json:"error_code"`
}

func (p *GCProvider) OwnershipHandoffHooks(ctx context.Context, r Request) (Hooks, error) {
	if p == nil {
		return Hooks{}, CodedError{Code: "provider_unavailable", Err: errors.New("GC provider is nil")}
	}
	if err := validateGCExecutable(p.Binary); err != nil {
		return Hooks{}, err
	}
	if r.CityRoot == "" {
		return Hooks{}, CodedError{Code: "invalid_request", Err: errors.New("city root is required by the GC handoff protocol")}
	}
	response, raw, err := p.invoke(ctx, r, "handoff-inspect", "")
	if err != nil {
		return Hooks{}, err
	}
	if response.Operation != "handoff-inspect" {
		return Hooks{}, CodedError{Code: "protocol_version", Err: errors.New("GC handoff inspect returned an unexpected operation")}
	}
	if response.Result != "eligible" {
		return Hooks{}, responseError(response, "handoff inspect refused")
	}
	if err := validateGCIdentity(r, response); err != nil {
		return Hooks{}, err
	}
	if err := validateIdentityToken(response.IdentityToken); err != nil {
		return Hooks{}, err
	}
	metadata := append([]byte(nil), raw...)
	identityToken := response.IdentityToken
	return Hooks{
		Snapshot: func(context.Context, Request) (Snapshot, error) {
			return Snapshot{Metadata: metadata, Sentinel: identityToken}, nil
		},
		// GC already owns target configuration. Configure is intentionally a
		// config-only no-op; it must never start a second server.
		Configure: func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(stopCtx context.Context, request Request, _ Snapshot) error {
			stopped, _, err := p.invoke(stopCtx, request, "handoff-stop", identityToken)
			if err != nil {
				return err
			}
			if stopped.Operation != "handoff-stop" {
				return CodedError{Code: "protocol_version", Err: errors.New("GC handoff stop returned an unexpected operation")}
			}
			if stopped.Result != "stopped" || !stopped.Mutates {
				return responseError(stopped, "GC handoff stop refused")
			}
			if err := validateGCIdentity(request, stopped); err != nil {
				return err
			}
			if stopped.IdentityToken != identityToken {
				return CodedError{Code: "identity_changed", Err: errors.New("GC handoff stop token changed")}
			}
			return nil
		},
		Verify:       func(context.Context, Request, Snapshot) error { return nil },
		Commit:       func(context.Context, Request, Snapshot) error { return nil },
		CommitReplay: func(context.Context, Request, Snapshot) error { return nil },
	}, nil
}

func (p *GCProvider) invoke(ctx context.Context, r Request, operation, token string) (gcHandoffResponse, []byte, error) {
	args := []string{"dolt-state", operation, "--json", "--city", r.CityRoot, "--scope-root", r.Root,
		"--database", r.Database, "--workspace", r.Workspace}
	if r.Endpoint.Socket != "" {
		args = append(args, "--socket", r.Endpoint.Socket)
	} else {
		args = append(args, "--host", r.Endpoint.Host, "--port", strconv.Itoa(r.Endpoint.Port))
	}
	if token != "" {
		args = append(args, "--identity-token", token)
	}
	cmd := exec.CommandContext(ctx, p.Binary, args...) // #nosec G204 -- Binary is validated by NewGCProvider.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	runErr := cmd.Run()
	response, err := decodeGCResponse(stdout.Bytes())
	if err != nil {
		if runErr != nil {
			return gcHandoffResponse{}, nil, CodedError{Code: "provider_unavailable", Err: errors.New("GC handoff protocol command failed")}
		}
		return gcHandoffResponse{}, nil, err
	}
	if runErr != nil && response.Result != "refused" {
		return gcHandoffResponse{}, nil, CodedError{Code: "provider_unavailable", Err: errors.New("GC handoff protocol command failed")}
	}
	return response, stdout.Bytes(), nil
}

func decodeGCResponse(raw []byte) (gcHandoffResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response gcHandoffResponse
	if err := decoder.Decode(&response); err != nil {
		return gcHandoffResponse{}, CodedError{Code: "protocol_version", Err: fmt.Errorf("decode GC handoff response: %w", err)}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return gcHandoffResponse{}, CodedError{Code: "protocol_version", Err: errors.New("GC handoff response must contain one JSON object")}
	}
	if response.SchemaVersion != gcHandoffSchemaVersion {
		return gcHandoffResponse{}, CodedError{Code: "protocol_version", Err: errors.New("unsupported GC handoff schema version")}
	}
	if response.Owner != OwnerLegacyGC {
		return gcHandoffResponse{}, CodedError{Code: "process_unowned", Err: errors.New("GC handoff owner is not legacy-gc")}
	}
	return response, nil
}

func responseError(response gcHandoffResponse, message string) error {
	code := stableErrorCode(response.ErrorCode, "provider_unavailable")
	return CodedError{Code: code, Err: errors.New(message)}
}

func validateIdentityToken(token string) error {
	const prefix = "sha256:"
	if len(token) != len(prefix)+sha256.Size*2 || token[:len(prefix)] != prefix {
		return CodedError{Code: "protocol_version", Err: errors.New("GC handoff identity token is not sha256 encoded")}
	}
	if _, err := hex.DecodeString(token[len(prefix):]); err != nil {
		return CodedError{Code: "protocol_version", Err: errors.New("GC handoff identity token is not hexadecimal")}
	}
	return nil
}

func validateGCIdentity(r Request, response gcHandoffResponse) error {
	i := response.Identity
	if i.CityRoot != r.CityRoot || i.ScopeRoot != r.Root || i.Database != r.Database || i.Workspace != r.Workspace || i.Endpoint != r.Endpoint {
		return CodedError{Code: "identity_changed", Err: errors.New("GC handoff identity does not match request")}
	}
	if i.PID <= 0 || i.StartIdentity == "" || i.StartTimeTicks < 0 || i.PortHolderPID < 0 {
		return CodedError{Code: "protocol_version", Err: errors.New("GC handoff identity has invalid process fields")}
	}
	for _, field := range []struct {
		name string
		path string
	}{
		{name: "data_dir", path: i.DataDir},
		{name: "config_file", path: i.ConfigFile},
	} {
		name, path := field.name, field.path
		if err := validateIdentityPath(r.CityRoot, path); err != nil {
			return CodedError{Code: "identity_changed", Err: fmt.Errorf("%s: %w", name, err)}
		}
	}
	return nil
}

func validateIdentityPath(root, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and canonical")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path must be beneath city root")
	}
	return nil
}
