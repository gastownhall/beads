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
	"time"
)

const gcHandoffSchemaVersion = 1

const defaultGCHandoffTimeout = 30 * time.Second

const maxGCHandoffProtocolOutput = 1 << 20

// GCProvider invokes the explicit hidden Gas City handoff protocol. The
// binary path must be absolute, canonical, and executable; no PATH lookup or
// process-state fallback is permitted.
type GCProvider struct {
	Binary  string
	timeout time.Duration
}

// NewGCProvider validates a trusted absolute GC executable path.
func NewGCProvider(binary string) (Provider, error) {
	canonical, err := canonicalGCExecutable(binary)
	if err != nil {
		return nil, err
	}
	return &GCProvider{Binary: canonical, timeout: defaultGCHandoffTimeout}, nil
}

func validateGCExecutable(binary string) error {
	_, err := canonicalGCExecutable(binary)
	return err
}

func canonicalGCExecutable(binary string) (string, error) {
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		return "", CodedError{Code: "provider_unavailable", Err: errors.New("GC_BIN must be an absolute canonical path")}
	}
	info, err := os.Lstat(binary)
	if err != nil {
		return "", CodedError{Code: "provider_unavailable", Err: fmt.Errorf("stat GC_BIN: %w", err)}
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", CodedError{Code: "provider_unavailable", Err: errors.New("GC_BIN must be an executable regular file")}
	}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return "", CodedError{Code: "provider_unavailable", Err: fmt.Errorf("resolve GC_BIN: %w", err)}
	}
	return real, nil
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
	return Hooks{
		Snapshot: func(snapshotCtx context.Context, request Request) (Snapshot, error) {
			response, raw, err := p.inspect(snapshotCtx, request)
			if err != nil {
				return Snapshot{}, err
			}
			return Snapshot{Metadata: append([]byte(nil), raw...), Sentinel: response.IdentityToken}, nil
		},
		// GC already owns target configuration. Configure is intentionally a
		// config-only no-op; it must never start a second server.
		Configure: func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(stopCtx context.Context, request Request, snapshot Snapshot) error {
			identityToken, err := snapshotIdentityToken(request, snapshot)
			if err != nil {
				return err
			}
			stopped, _, err := p.invoke(stopCtx, request, "handoff-stop", identityToken)
			if err != nil {
				return err
			}
			if stopped.Operation != "handoff-stop" {
				return CodedError{Code: "protocol_version", Err: errors.New("GC handoff stop returned an unexpected operation")}
			}
			if stopped.Result != "stopped" || !stopped.Mutates {
				return withReportedMutation(responseError(stopped, "GC handoff stop refused"), stopped.Mutates)
			}
			if err := validateGCIdentity(request, stopped); err != nil {
				return withReportedMutation(err, stopped.Mutates)
			}
			if stopped.IdentityToken != identityToken {
				return withReportedMutation(CodedError{Code: "identity_changed", Err: errors.New("GC handoff stop token changed")}, stopped.Mutates)
			}
			return nil
		},
		Verify: func(verifyCtx context.Context, request Request, _ Snapshot) error {
			return p.verifyStopped(verifyCtx, request)
		},
		Commit: func(commitCtx context.Context, request Request, _ Snapshot) error {
			return p.verifyStopped(commitCtx, request)
		},
		CommitReplay: func(commitCtx context.Context, request Request, _ Snapshot) error {
			return p.verifyStopped(commitCtx, request)
		},
	}, nil
}

func (p *GCProvider) inspect(ctx context.Context, r Request) (gcHandoffResponse, []byte, error) {
	response, raw, err := p.invoke(ctx, r, "handoff-inspect", "")
	if err != nil {
		return gcHandoffResponse{}, nil, err
	}
	if response.Operation != "handoff-inspect" {
		return gcHandoffResponse{}, nil, CodedError{Code: "protocol_version", Err: errors.New("GC handoff inspect returned an unexpected operation")}
	}
	if response.Result != "eligible" {
		return gcHandoffResponse{}, nil, responseError(response, "handoff inspect refused")
	}
	if err := validateGCIdentity(r, response); err != nil {
		return gcHandoffResponse{}, nil, err
	}
	if err := validateIdentityToken(response.IdentityToken); err != nil {
		return gcHandoffResponse{}, nil, err
	}
	return response, raw, nil
}

func snapshotIdentityToken(r Request, snapshot Snapshot) (string, error) {
	response, err := decodeGCResponse(snapshot.Metadata)
	if err != nil {
		return "", err
	}
	if response.Operation != "handoff-inspect" || response.Result != "eligible" {
		return "", CodedError{Code: "protocol_version", Err: errors.New("handoff snapshot is not an eligible inspect response")}
	}
	if err := validateGCIdentity(r, response); err != nil {
		return "", err
	}
	if err := validateIdentityToken(response.IdentityToken); err != nil {
		return "", err
	}
	if snapshot.Sentinel != response.IdentityToken {
		return "", CodedError{Code: "identity_changed", Err: errors.New("handoff snapshot token does not match its inspect response")}
	}
	return response.IdentityToken, nil
}

// verifyStopped asks the lifecycle owner to re-inspect the captured scope.
// A post-stop inspect must refuse with process_missing; any eligible or other
// response means the legacy owner has not proven that it released the scope.
func (p *GCProvider) verifyStopped(ctx context.Context, r Request) error {
	response, _, err := p.invoke(ctx, r, "handoff-inspect", "")
	if err != nil {
		return err
	}
	if response.Operation != "handoff-inspect" {
		return CodedError{Code: "protocol_version", Err: errors.New("GC handoff verification returned an unexpected operation")}
	}
	if response.Result == "refused" && response.ErrorCode == "process_missing" {
		return nil
	}
	return CodedError{Code: "verification_failed", Err: errors.New("GC handoff verification did not confirm the legacy owner stopped")}
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
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultGCHandoffTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, p.Binary, args...) // #nosec G204 -- Binary is validated by NewGCProvider.
	configureGCHandoffCommand(cmd)
	stdout := &limitedBuffer{limit: maxGCHandoffProtocolOutput}
	stderr := &limitedBuffer{limit: maxGCHandoffProtocolOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	response, err := decodeGCResponse(stdout.Bytes())
	if err != nil {
		if runErr != nil {
			return gcHandoffResponse{}, nil, CodedError{Code: "provider_unavailable", Err: protocolCommandError(commandCtx, runErr, stderr.String())}
		}
		return gcHandoffResponse{}, nil, err
	}
	if runErr != nil && response.Result != "refused" {
		return gcHandoffResponse{}, nil, CodedError{Code: "provider_unavailable", Err: protocolCommandError(commandCtx, runErr, stderr.String())}
	}
	return response, stdout.Bytes(), nil
}

func protocolCommandError(ctx context.Context, runErr error, stderr string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("GC handoff protocol command timed out")
	}
	if detail := strings.TrimSpace(stderr); detail != "" {
		return fmt.Errorf("GC handoff protocol command failed: %s", detail)
	}
	return fmt.Errorf("GC handoff protocol command failed: %w", runErr)
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 || len(p) > remaining {
		return 0, errors.New("GC handoff protocol output exceeds limit")
	}
	return b.Buffer.Write(p)
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
