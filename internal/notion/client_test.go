package notion

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/notion/output"
	"github.com/steveyegge/beads/internal/notion/wire"
)

type fakeClientService struct {
	statusReq struct {
		databaseID string
		viewURL    string
	}
	pullReq struct {
		cacheMaxAge time.Duration
	}
	pushReq struct {
		payload        []byte
		databaseID     string
		viewURL        string
		dryRun         bool
		archiveMissing bool
		cacheMaxAge    time.Duration
	}

	statusResp *serviceStatusResponse
	pullResp   *servicePullResponse
	pushResp   *servicePushResponse
	err        error
}

func (f *fakeClientService) StatusResponse(_ context.Context, databaseID, viewURL string) (*serviceStatusResponse, error) {
	f.statusReq.databaseID = databaseID
	f.statusReq.viewURL = viewURL
	return f.statusResp, f.err
}

func (f *fakeClientService) PullResponse(_ context.Context, cacheMaxAge time.Duration) (*servicePullResponse, error) {
	f.pullReq.cacheMaxAge = cacheMaxAge
	return f.pullResp, f.err
}

func (f *fakeClientService) PushPayloadResponse(_ context.Context, payload []byte, databaseID, viewURL string, dryRun, archiveMissing bool, cacheMaxAge time.Duration) (*servicePushResponse, error) {
	f.pushReq.payload = append([]byte(nil), payload...)
	f.pushReq.databaseID = databaseID
	f.pushReq.viewURL = viewURL
	f.pushReq.dryRun = dryRun
	f.pushReq.archiveMissing = archiveMissing
	f.pushReq.cacheMaxAge = cacheMaxAge
	return f.pushResp, f.err
}

func newFakeClient(t *testing.T, svc *fakeClientService, factoryErr error) *Client {
	t.Helper()
	return NewClient(WithServiceFactory(func(_ io.Writer) (serviceClient, error) {
		if factoryErr != nil {
			return nil, factoryErr
		}
		return svc, nil
	}))
}

func TestStatusUsesServiceAndMapsTypedResponse(t *testing.T) {
	t.Parallel()

	svc := &fakeClientService{
		statusResp: &serviceStatusResponse{
			Ready:        true,
			DataSourceID: "ds_123",
			SavedConfig:  true,
		},
	}
	client := newFakeClient(t, svc, nil)
	resp, err := client.Status(context.Background(), StatusRequest{
		DatabaseID: "db_123",
		ViewURL:    "https://example.com/view",
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !resp.Ready {
		t.Fatal("ready = false, want true")
	}
	if svc.statusReq.databaseID != "db_123" {
		t.Fatalf("database id = %q", svc.statusReq.databaseID)
	}
	if svc.statusReq.viewURL != "https://example.com/view" {
		t.Fatalf("view url = %q", svc.statusReq.viewURL)
	}
}

func TestStatusReturnsStructuredServiceError(t *testing.T) {
	t.Parallel()

	svc := &fakeClientService{
		err: output.NewError("Not authenticated", "could not authenticate against the Notion MCP", "Run \"bd notion login\" again", 1),
	}
	client := newFakeClient(t, svc, nil)
	_, err := client.Status(context.Background(), StatusRequest{})

	var bridgeErr *BridgeCLIError
	if !errors.As(err, &bridgeErr) {
		t.Fatalf("expected BridgeCLIError, got %T (%v)", err, err)
	}
	if bridgeErr.What != "Not authenticated" {
		t.Fatalf("What = %q", bridgeErr.What)
	}
	if bridgeErr.Hint != "Run \"bd notion login\" again" {
		t.Fatalf("Hint = %q", bridgeErr.Hint)
	}
}

func TestPullPassesCacheMaxAgeToService(t *testing.T) {
	t.Parallel()

	svc := &fakeClientService{pullResp: &servicePullResponse{Issues: []wire.Issue{}}}
	client := newFakeClient(t, svc, nil)
	if _, err := client.Pull(context.Background(), PullRequest{CacheMaxAge: 5 * time.Minute}); err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}
	if svc.pullReq.cacheMaxAge != 5*time.Minute {
		t.Fatalf("cache max age = %s", svc.pullReq.cacheMaxAge)
	}
}

func TestPushRequiresPayload(t *testing.T) {
	t.Parallel()

	client := newFakeClient(t, &fakeClientService{}, nil)
	_, err := client.Push(context.Background(), PushRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "payload is required") {
		t.Fatalf("error = %q, want payload is required", err.Error())
	}
}

func TestPushPassesPayloadAndOverridesToService(t *testing.T) {
	t.Parallel()

	svc := &fakeClientService{
		pushResp: &servicePushResponse{
			DryRun:           false,
			ArchiveRequested: false,
			ArchiveSupported: false,
			InputCount:       1,
			CreatedCount:     1,
			UpdatedCount:     0,
			SkippedCount:     0,
		},
	}
	client := newFakeClient(t, svc, nil)
	payload := []byte(`{"issues":[{"id":"bd-1","title":"One"}]}`)
	resp, err := client.Push(context.Background(), PushRequest{
		DatabaseID:  "db_123",
		ViewURL:     "https://example.com/view",
		Payload:     payload,
		CacheMaxAge: 3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	if resp.CreatedCount != 1 {
		t.Fatalf("created count = %d", resp.CreatedCount)
	}
	if string(svc.pushReq.payload) != string(payload) {
		t.Fatalf("payload = %q", string(svc.pushReq.payload))
	}
	if svc.pushReq.databaseID != "db_123" {
		t.Fatalf("database id = %q", svc.pushReq.databaseID)
	}
	if svc.pushReq.viewURL != "https://example.com/view" {
		t.Fatalf("view url = %q", svc.pushReq.viewURL)
	}
	if svc.pushReq.cacheMaxAge != 3*time.Minute {
		t.Fatalf("cache max age = %s", svc.pushReq.cacheMaxAge)
	}
	if svc.pushReq.dryRun {
		t.Fatal("dryRun = true, want false")
	}
	if svc.pushReq.archiveMissing {
		t.Fatal("archiveMissing = true, want false")
	}
}

func TestStatusReturnsFactoryErrorAsCommandError(t *testing.T) {
	t.Parallel()

	client := newFakeClient(t, &fakeClientService{}, errors.New("boom"))
	_, err := client.Status(context.Background(), StatusRequest{})

	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T (%v)", err, err)
	}
	if !strings.Contains(cmdErr.Error(), "boom") {
		t.Fatalf("error = %v", cmdErr)
	}
}
