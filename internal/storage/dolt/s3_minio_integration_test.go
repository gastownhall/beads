// cgo: the embedded engine (embeddeddolt) is cgo-only; see #6235 for the CI
// gap that let the missing tag through.
//
//go:build integration && cgo

package dolt_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	doltstore "github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

const (
	minioRootUser     = "beads-minio"
	minioRootPassword = "beads-minio-secret"
	minioRegion       = "us-east-1"
	checksumSignature = "Response has no supported checksum"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// s3Credentials is what the test's own SigV4 signer signs with. Dolt's AWS
// SDK chain still reads AWS_* from the environment, so startMinio exports the
// same values there; the signer never does.
type s3Credentials struct {
	accessKey string
	secretKey string
	region    string
}

type minioServer struct {
	addr     string
	endpoint string
	creds    s3Credentials
	cmd      *exec.Cmd
	done     chan error
	logs     lockedBuffer
}

func startMinio(t *testing.T) *minioServer {
	t.Helper()
	creds := s3Credentials{accessKey: minioRootUser, secretKey: minioRootPassword, region: minioRegion}
	t.Setenv("MINIO_ROOT_USER", creds.accessKey)
	t.Setenv("MINIO_ROOT_PASSWORD", creds.secretKey)
	t.Setenv("AWS_ACCESS_KEY_ID", creds.accessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", creds.secretKey)
	t.Setenv("AWS_REGION", creds.region)

	binary := minioBinary(t)
	var attempts strings.Builder
	for attempt := 1; attempt <= 3; attempt++ {
		port, err := testutil.FindFreePort()
		if err != nil {
			t.Fatalf("find free MinIO port: %v", err)
		}
		server := &minioServer{
			addr:     fmt.Sprintf("127.0.0.1:%d", port),
			endpoint: fmt.Sprintf("http://127.0.0.1:%d", port),
			creds:    creds,
			done:     make(chan error, 1),
		}
		server.cmd = exec.Command(binary, "server", t.TempDir(), "--address", server.addr) // #nosec G204 -- binary is explicitly configured for test setup
		server.cmd.Env = os.Environ()
		server.cmd.Stdout = &server.logs
		server.cmd.Stderr = &server.logs
		if err := server.cmd.Start(); err != nil {
			fmt.Fprintf(&attempts, "attempt %d start: %v\n", attempt, err)
			continue
		}
		go func() { server.done <- server.cmd.Wait() }()
		if waitForMinio(server, 5*time.Second) {
			t.Cleanup(func() {
				if err := server.stop(); err != nil {
					t.Error(err)
				}
				if t.Failed() {
					t.Logf("MinIO logs:\n%s", server.logs.String())
				}
			})
			return server
		}
		fmt.Fprintf(&attempts, "attempt %d at %s:\n%s\n", attempt, server.addr, server.logs.String())
		if err := server.stop(); err != nil {
			fmt.Fprintf(&attempts, "attempt %d cleanup: %v\n", attempt, err)
		}
	}
	t.Fatalf("MinIO did not become ready after three attempts:\n%s", attempts.String())
	return nil
}

func waitForMinio(server *minioServer, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(server.endpoint + "/minio/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func minioBinary(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("BEADS_MINIO_BIN"); configured != "" {
		info, err := os.Stat(configured)
		if err != nil {
			t.Fatalf("BEADS_MINIO_BIN %q: %v", configured, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			t.Fatalf("BEADS_MINIO_BIN %q is not an executable file", configured)
		}
		return configured
	}
	if binary, err := exec.LookPath("minio"); err == nil {
		return binary
	}
	t.Skip("MinIO binary unavailable: set BEADS_MINIO_BIN or install minio on PATH")
	return ""
}

func requireS3DoltCLI(t *testing.T) {
	t.Helper()
	testutil.RequireDoltBinary(t)
	if reason := s3DoltCLIProbeFailure(t); reason != "" {
		t.Skipf("server-mode s3 replication needs a dolt CLI with s3:// support (first release: v2.3.2); %s", reason)
	}
}

// s3DoltCLIProbeFailure runs the capability probes against the dolt CLI on
// PATH and returns the first failure reason, or "" when the CLI can address
// an s3:// remote.
func s3DoltCLIProbeFailure(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("dolt")
	if err != nil {
		return err.Error()
	}

	probeDir := t.TempDir()
	init := exec.Command(binary, "init") // #nosec G204 -- binary is resolved from PATH for an explicit capability probe
	init.Dir = probeDir
	if output, err := init.CombinedOutput(); err != nil {
		return fmt.Sprintf("dolt init probe failed: %v\n%s", err, output)
	}
	probe := exec.Command(binary, "remote", "add", "s3-capability-probe", "s3://beads/probe?endpoint=http://127.0.0.1:1&region=us-east-1&path-style=true") // #nosec G204 -- fixed capability probe arguments
	probe.Dir = probeDir
	if output, err := probe.CombinedOutput(); err != nil {
		return fmt.Sprintf("probe rejected s3://: %v\n%s", err, output)
	}
	fetch := exec.Command(binary, "fetch", "s3-capability-probe") // #nosec G204 -- fixed capability probe arguments
	fetch.Dir = probeDir
	if output, err := fetch.CombinedOutput(); err != nil && strings.Contains(strings.ToLower(string(output)), "unknown url scheme") {
		return fmt.Sprintf("probe could not fetch s3://: %v\n%s", err, output)
	}
	return ""
}

func (s *minioServer) stop() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("MinIO process did not exit within 5s")
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("MinIO listener leaked on %s: %w", s.addr, err)
	}
	_ = listener.Close()
	return nil
}

func (s *minioServer) s3URL(bucket, path string) string {
	return fmt.Sprintf("s3://%s/%s?endpoint=%s&region=%s&path-style=true", bucket, path, s.endpoint, minioRegion)
}

func (s *minioServer) createBucket(t *testing.T, ctx context.Context, bucket string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.endpoint+"/"+bucket, nil)
		if err != nil {
			t.Fatalf("create bucket request: %v", err)
		}
		signS3Request(t, req, nil, s.creds)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			t.Fatalf("create MinIO bucket %q: %v", bucket, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read MinIO bucket response: %v", readErr)
		}
		if resp.StatusCode == http.StatusOK {
			return
		}
		if resp.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(body), "XMinioServerNotInitialized") && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		t.Fatalf("create MinIO bucket %q: status %s: %s", bucket, resp.Status, body)
	}
}

func (s *minioServer) assertBucketHasObjects(t *testing.T, ctx context.Context, bucket string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+"/"+bucket+"?list-type=2", nil)
	if err != nil {
		t.Fatalf("list MinIO bucket request: %v", err)
	}
	signS3Request(t, req, nil, s.creds)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list MinIO bucket %q: %v", bucket, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read MinIO bucket %q listing: %v", bucket, readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list MinIO bucket %q: status %s: %s", bucket, resp.Status, body)
	}
	if !strings.Contains(string(body), "<Key>") {
		t.Fatalf("MinIO bucket %q has no remote objects after push: %s", bucket, body)
	}
}

func signS3Request(t *testing.T, req *http.Request, body []byte, creds s3Credentials) {
	t.Helper()
	payloadHash := sha256Hex(body)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	host := req.URL.Host
	req.Host = host
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := req.URL.Query().Encode()
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	scope := strings.Join([]string{date, creds.region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signingKey := hmacSHA256([]byte("AWS4"+creds.secretKey), date)
	signingKey = hmacSHA256(signingKey, creds.region)
	signingKey = hmacSHA256(signingKey, "s3")
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.accessKey, scope, signedHeaders, signature,
	))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func uniqueS3Database(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().UTC().String()))
	return "s3_" + hex.EncodeToString(sum[:4])
}

type s3DoltServer struct {
	port int
	cmd  *exec.Cmd
	done chan error
	logs lockedBuffer
}

func startS3DoltServer(t *testing.T, dataDir string) *s3DoltServer {
	t.Helper()
	var attempts strings.Builder
	for attempt := 1; attempt <= 3; attempt++ {
		port, err := testutil.FindFreePort()
		if err != nil {
			t.Fatalf("find free Dolt port: %v", err)
		}
		server := &s3DoltServer{port: port, done: make(chan error, 1)}
		server.cmd = exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", fmt.Sprint(port)) // #nosec G204 -- fixed Dolt server command
		server.cmd.Dir = dataDir
		server.cmd.Env = doltserver.ServerSpawnEnv()
		server.cmd.Stdout = &server.logs
		server.cmd.Stderr = &server.logs
		if err := server.cmd.Start(); err != nil {
			fmt.Fprintf(&attempts, "attempt %d start: %v\n", attempt, err)
			continue
		}
		go func() { server.done <- server.cmd.Wait() }()
		if testutil.WaitForServer(port, 5*time.Second) {
			t.Cleanup(func() {
				if err := server.stop(); err != nil {
					t.Error(err)
				}
			})
			return server
		}
		fmt.Fprintf(&attempts, "attempt %d on port %d:\n%s\n", attempt, port, server.logs.String())
		if err := server.stop(); err != nil {
			fmt.Fprintf(&attempts, "attempt %d cleanup: %v\n", attempt, err)
		}
	}
	t.Fatalf("Dolt SQL server did not become ready after three attempts:\n%s", attempts.String())
	return nil
}

func (s *s3DoltServer) stop() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("Dolt SQL server on port %d did not exit within 5s", s.port)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return fmt.Errorf("Dolt SQL server listener leaked on port %d: %w", s.port, err)
	}
	_ = listener.Close()
	return nil
}

func openS3Store(t *testing.T, ctx context.Context, dataDir, database string, server *s3DoltServer) *doltstore.DoltStore {
	t.Helper()
	store, err := doltstore.New(ctx, &doltstore.Config{
		Path:            dataDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      server.port,
		ServerUser:      "root",
		AutoStart:       false,
		CommitterName:   "s3-test",
		CommitterEmail:  "s3-test@example.com",
		Database:        database,
		Remote:          "origin",
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("open Dolt store %q: %v", database, err)
	}
	return store
}

func newS3Store(t *testing.T, ctx context.Context, dataDir, database string, server *s3DoltServer) *doltstore.DoltStore {
	t.Helper()
	store := openS3Store(t, ctx, dataDir, database, server)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		_ = store.Close()
		t.Fatalf("set issue prefix: %v", err)
	}
	if _, err := store.CommitAll(ctx, "Genesis: schema and config"); err != nil {
		_ = store.Close()
		t.Fatalf("commit initial schema and config: %v", err)
	}
	return store
}

var stderrCaptureMu sync.Mutex

func captureS3OperationStderr(t *testing.T, operation func() error) (string, error) {
	t.Helper()
	stderrCaptureMu.Lock()
	defer stderrCaptureMu.Unlock()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("capture stderr: %v", err)
	}
	previousStderr := os.Stderr
	previousLogWriter := log.Writer()
	os.Stderr = writer
	log.SetOutput(writer)
	output := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		_ = reader.Close()
		output <- string(data)
	}()

	err = operation()
	os.Stderr = previousStderr
	log.SetOutput(previousLogWriter)
	_ = writer.Close()
	return <-output, err
}

func assertNoChecksumSignature(t *testing.T, outputs ...string) {
	t.Helper()
	for _, output := range outputs {
		if strings.Contains(output, checksumSignature) {
			t.Fatalf("S3 operation reported unsupported response checksum:\n%s", output)
		}
	}
}

type remoteLister interface {
	ListRemotes(context.Context) ([]storage.RemoteInfo, error)
}

type issueReader interface {
	GetIssue(context.Context, string) (*types.Issue, error)
}

type statisticsReader interface {
	GetStatistics(context.Context) (*types.Statistics, error)
}

func remoteURL(t *testing.T, store remoteLister, name string) string {
	t.Helper()
	remotes, err := store.ListRemotes(context.Background())
	if err != nil {
		t.Fatalf("list remotes: %v", err)
	}
	for _, remote := range remotes {
		if remote.Name == name {
			return remote.URL
		}
	}
	t.Fatalf("remote %q not found", name)
	return ""
}

func issueCount(t *testing.T, ctx context.Context, store statisticsReader) int {
	t.Helper()
	statistics, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("get issue statistics: %v", err)
	}
	return statistics.TotalIssues
}

func requireIssue(t *testing.T, ctx context.Context, store issueReader, id, title, description string) {
	t.Helper()
	issue, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%q): %v", id, err)
	}
	if issue == nil {
		t.Fatalf("issue %q was not found", id)
	}
	if issue.Title != title || issue.Description != description {
		t.Fatalf("issue %q = title %q description %q, want title %q description %q", id, issue.Title, issue.Description, title, description)
	}
}

func TestS3MinioReplicationRoundTrip(t *testing.T) {
	requireS3DoltCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	minio := startMinio(t)
	minio.createBucket(t, ctx, "beads")
	remote := minio.s3URL("beads", "replication")
	baseDir := t.TempDir()
	sourceDir := filepath.Join(baseDir, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	sourceServer := startS3DoltServer(t, sourceDir)
	sourceDB := uniqueS3Database(t)
	source := newS3Store(t, ctx, sourceDir, sourceDB, sourceServer)

	stateA := &types.Issue{ID: "s3-rep-a", Title: "S3 replication state A", Description: "first remote state", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}
	if err := source.CreateIssue(ctx, stateA, "tester"); err != nil {
		t.Fatalf("create state A issue: %v", err)
	}
	if err := source.Commit(ctx, "Add S3 replication state A"); err != nil {
		t.Fatalf("commit state A: %v", err)
	}
	if err := source.AddRemote(ctx, "origin", remote); err != nil {
		t.Fatalf("add S3 remote: %v", err)
	}
	if stored := remoteURL(t, source, "origin"); stored != remote {
		t.Fatalf("stored S3 remote = %q, want exact URL %q", stored, remote)
	}
	sourceHeadA, err := source.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read source state A ref: %v", err)
	}
	pushOutput, err := captureS3OperationStderr(t, func() error { return source.Push(ctx) })
	if err != nil {
		t.Fatalf("push state A: %v", err)
	}
	assertNoChecksumSignature(t, pushOutput, sourceServer.logs.String())
	if err := source.Close(); err != nil {
		t.Fatalf("close source after state A: %v", err)
	}

	cloneDir := filepath.Join(baseDir, "clone")
	cloneDB := uniqueS3Database(t)
	bootstrapped, err := doltstore.BootstrapFromRemoteWithDB(ctx, cloneDir, remote, cloneDB)
	if err != nil {
		t.Fatalf("bootstrap S3 clone: %v", err)
	}
	if !bootstrapped {
		t.Fatal("S3 clone bootstrap was skipped")
	}
	cloneServer := startS3DoltServer(t, cloneDir)
	clone := openS3Store(t, ctx, cloneDir, cloneDB, cloneServer)
	cloneHeadA, err := clone.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read clone state A ref: %v", err)
	}
	if cloneHeadA != sourceHeadA {
		t.Fatalf("clone state A ref = %s, want source ref %s", cloneHeadA, sourceHeadA)
	}
	requireIssue(t, ctx, clone, stateA.ID, stateA.Title, stateA.Description)
	if err := clone.Close(); err != nil {
		t.Fatalf("close clone before source mutation: %v", err)
	}

	source = openS3Store(t, ctx, sourceDir, sourceDB, sourceServer)
	stateB := &types.Issue{ID: "s3-rep-b", Title: "S3 replication state B", Description: "replacement manifest state", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}
	if err := source.CreateIssue(ctx, stateB, "tester"); err != nil {
		t.Fatalf("create state B issue: %v", err)
	}
	if err := source.Commit(ctx, "Add S3 replication state B"); err != nil {
		t.Fatalf("commit state B: %v", err)
	}
	sourceHeadB, err := source.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read source state B ref: %v", err)
	}
	pushOutput, err = captureS3OperationStderr(t, func() error { return source.Push(ctx) })
	if err != nil {
		t.Fatalf("push state B: %v", err)
	}
	assertNoChecksumSignature(t, pushOutput, sourceServer.logs.String())
	if err := source.Close(); err != nil {
		t.Fatalf("close source after state B: %v", err)
	}

	clone = openS3Store(t, ctx, cloneDir, cloneDB, cloneServer)
	pullOutput, err := captureS3OperationStderr(t, func() error { return clone.Pull(ctx) })
	if err != nil {
		t.Fatalf("pull state B: %v", err)
	}
	assertNoChecksumSignature(t, pullOutput, cloneServer.logs.String())
	cloneHeadB, err := clone.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read clone state B ref: %v", err)
	}
	if cloneHeadB != sourceHeadB {
		t.Fatalf("clone state B ref = %s, want source ref %s", cloneHeadB, sourceHeadB)
	}
	requireIssue(t, ctx, clone, stateA.ID, stateA.Title, stateA.Description)
	requireIssue(t, ctx, clone, stateB.ID, stateB.Title, stateB.Description)
	if got := issueCount(t, ctx, clone); got != 2 {
		t.Fatalf("clone issue count after state B = %d, want 2", got)
	}
	if err := clone.Close(); err != nil {
		t.Fatalf("close clone after pull: %v", err)
	}
}

func TestS3MinioEmbeddedBackupRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	minio := startMinio(t)
	minio.createBucket(t, ctx, "beads")
	backupURL := minio.s3URL("beads", "backup")
	baseDir := t.TempDir()
	source := newEmbeddedS3Store(t, ctx, filepath.Join(baseDir, "source", ".beads"), "source")
	issues := []*types.Issue{
		{ID: "s3-backup-a", Title: "S3 backup first issue", Description: "backup content A", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2},
		{ID: "s3-backup-b", Title: "S3 backup second issue", Description: "backup content B", IssueType: types.TypeBug, Status: types.StatusInProgress, Priority: 1},
	}
	for _, issue := range issues {
		if err := source.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create backup issue %q: %v", issue.ID, err)
		}
	}
	if err := source.Commit(ctx, "Create S3 backup issues"); err != nil {
		t.Fatalf("commit backup source: %v", err)
	}
	if err := source.BackupAdd(ctx, "s3backup", backupURL); err != nil {
		t.Fatalf("backup init: %v", err)
	}
	if err := source.BackupSync(ctx, "s3backup"); err != nil {
		t.Fatalf("backup sync: %v", err)
	}
	sourceCount := issueCount(t, ctx, source)
	if err := source.Close(); err != nil {
		t.Fatalf("close backup source: %v", err)
	}

	restored := newEmbeddedS3Store(t, ctx, filepath.Join(baseDir, "restore", ".beads"), "restore")
	if got := issueCount(t, ctx, restored); got != 0 {
		t.Fatalf("fresh restore store has %d issues, want 0", got)
	}
	if err := restored.RestoreDatabase(ctx, backupURL, true); err != nil {
		t.Fatalf("restore S3 backup: %v", err)
	}
	if got := issueCount(t, ctx, restored); got != sourceCount {
		t.Fatalf("restored issue count = %d, want source count %d", got, sourceCount)
	}
	requireIssue(t, ctx, restored, issues[1].ID, issues[1].Title, issues[1].Description)
	if err := restored.Close(); err != nil {
		t.Fatalf("close restored store: %v", err)
	}
}

func TestS3MinioEmbeddedPush(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	minio := startMinio(t)
	minio.createBucket(t, ctx, "beads")
	remote := minio.s3URL("beads", "embedded-push")
	store := newEmbeddedS3Store(t, ctx, filepath.Join(t.TempDir(), ".beads"), "embedded_push")

	stateA := &types.Issue{ID: "s3-embedded-push-a", Title: "Embedded S3 push state A", Description: "first embedded remote state", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}
	if err := store.CreateIssue(ctx, stateA, "tester"); err != nil {
		t.Fatalf("create state A issue: %v", err)
	}
	if err := store.Commit(ctx, "Add embedded S3 push state A"); err != nil {
		t.Fatalf("commit state A: %v", err)
	}
	if err := store.AddRemote(ctx, "origin", remote); err != nil {
		t.Fatalf("add embedded S3 remote: %v", err)
	}
	if stored := remoteURL(t, store, "origin"); stored != remote {
		t.Fatalf("stored embedded S3 remote = %q, want exact URL %q", stored, remote)
	}
	pushOutput, err := captureS3OperationStderr(t, func() error { return store.Push(ctx) })
	if err != nil {
		t.Fatalf("push embedded state A: %v", err)
	}
	assertNoChecksumSignature(t, pushOutput)
	minio.assertBucketHasObjects(t, ctx, "beads")

	stateB := &types.Issue{ID: "s3-embedded-push-b", Title: "Embedded S3 push state B", Description: "replacement manifest state", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}
	if err := store.CreateIssue(ctx, stateB, "tester"); err != nil {
		t.Fatalf("create state B issue: %v", err)
	}
	if err := store.Commit(ctx, "Add embedded S3 push state B"); err != nil {
		t.Fatalf("commit state B: %v", err)
	}
	pushOutput, err = captureS3OperationStderr(t, func() error { return store.Push(ctx) })
	if err != nil {
		t.Fatalf("push embedded state B: %v", err)
	}
	assertNoChecksumSignature(t, pushOutput)
	minio.assertBucketHasObjects(t, ctx, "beads")
}

func newEmbeddedS3Store(t *testing.T, ctx context.Context, beadsDir, database string) *embeddeddolt.EmbeddedDoltStore {
	t.Helper()
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open embedded Dolt store %q: %v", database, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		_ = store.Close()
		t.Fatalf("set embedded issue prefix: %v", err)
	}
	if _, err := store.CommitAll(ctx, "Genesis: schema and config"); err != nil {
		_ = store.Close()
		t.Fatalf("commit embedded schema and config: %v", err)
	}
	return store
}
