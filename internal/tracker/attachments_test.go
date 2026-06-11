package tracker

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

type attachmentCapableMockTracker struct {
	*mockTracker
	settings    *AttachmentSettings
	remote      []TrackerAttachment
	uploadCalls int
}

func (m *attachmentCapableMockTracker) AttachmentSettings(context.Context) (*AttachmentSettings, error) {
	return m.settings, nil
}

func (m *attachmentCapableMockTracker) FetchIssueAttachments(context.Context, *TrackerIssue) ([]TrackerAttachment, error) {
	return append([]TrackerAttachment(nil), m.remote...), nil
}

func (m *attachmentCapableMockTracker) DownloadAttachment(context.Context, TrackerAttachment) ([]byte, error) {
	return nil, nil
}

func (m *attachmentCapableMockTracker) UploadAttachment(context.Context, string, *types.Attachment, []byte) (*TrackerAttachment, error) {
	m.uploadCalls++
	return &TrackerAttachment{ID: "remote-1"}, nil
}

type attachmentListStore struct {
	*pureTestStore
	attachments []*types.Attachment
}

func (s *attachmentListStore) ListAttachments(context.Context, string) ([]*types.Attachment, error) {
	return append([]*types.Attachment(nil), s.attachments...), nil
}

func TestAttachmentSyncCapabilityGating(t *testing.T) {
	engine := NewEngine(newMockTracker("mock"), newPureTestStore(), "tester")

	pullStats := engine.syncPullAttachments(context.Background(), "bd-1", &TrackerIssue{Identifier: "EXT-1"}, SyncOptions{PullAttachments: true})
	if pullStats.skipped != 1 || pullStats.errors != 0 {
		t.Fatalf("pullStats = %+v, want one skipped and no errors", pullStats)
	}

	pushStats := engine.syncPushAttachments(context.Background(), &types.Issue{ID: "bd-1", Title: "Issue"}, "EXT-1", SyncOptions{PushAttachments: true})
	if pushStats.skipped != 1 || pushStats.errors != 0 {
		t.Fatalf("pushStats = %+v, want one skipped and no errors", pushStats)
	}
}

func TestMetadataWithTrackerAttachmentsPreservesRemoteIdentity(t *testing.T) {
	got := metadataWithTrackerAttachments(map[string]interface{}{"source_system": "jira:PROJ:PROJ-1"}, []TrackerAttachment{{
		ID:         "10001",
		Filename:   "body.md",
		ContentURL: "https://example.atlassian.net/rest/api/3/attachment/content/10001",
		ByteSize:   42,
	}})

	if got["source_system"] != "jira:PROJ:PROJ-1" {
		t.Fatalf("source metadata missing: %+v", got)
	}
	raw, ok := got["tracker_attachments"].([]TrackerAttachment)
	if !ok || len(raw) != 1 {
		t.Fatalf("tracker_attachments = %#v", got["tracker_attachments"])
	}
	if raw[0].ID != "10001" || raw[0].ContentURL == "" {
		t.Fatalf("remote identity not preserved: %+v", raw[0])
	}
}

func TestAttachmentPushSkipsDisabledAndOversizedUploads(t *testing.T) {
	issue := &types.Issue{ID: "bd-1", Title: "Issue"}
	localAttachment := &types.Attachment{
		IssueID:          issue.ID,
		OriginalFilename: "large.bin",
		ByteSize:         2048,
		StorageRelPath:   "attachments/bd-1/hash",
	}

	disabled := &attachmentCapableMockTracker{
		mockTracker: newMockTracker("mock"),
		settings:    &AttachmentSettings{Enabled: false, UploadLimit: 1024},
	}
	disabledEngine := NewEngine(disabled, &attachmentListStore{pureTestStore: newPureTestStore(issue), attachments: []*types.Attachment{localAttachment}}, "tester")
	disabledStats := disabledEngine.syncPushAttachments(context.Background(), issue, "EXT-1", SyncOptions{PushAttachments: true})
	if disabledStats.skipped != 1 || disabled.uploadCalls != 0 {
		t.Fatalf("disabled stats = %+v uploadCalls=%d", disabledStats, disabled.uploadCalls)
	}

	oversized := &attachmentCapableMockTracker{
		mockTracker: newMockTracker("mock"),
		settings:    &AttachmentSettings{Enabled: true, UploadLimit: 1024},
	}
	oversizedEngine := NewEngine(oversized, &attachmentListStore{pureTestStore: newPureTestStore(issue), attachments: []*types.Attachment{localAttachment}}, "tester")
	oversizedStats := oversizedEngine.syncPushAttachments(context.Background(), issue, "EXT-1", SyncOptions{PushAttachments: true})
	if oversizedStats.skipped != 1 || oversized.uploadCalls != 0 {
		t.Fatalf("oversized stats = %+v uploadCalls=%d", oversizedStats, oversized.uploadCalls)
	}
}

func TestAttachmentPushSkipsExistingRemoteFilenameSize(t *testing.T) {
	issue := &types.Issue{ID: "bd-1", Title: "Issue"}
	localAttachment := &types.Attachment{
		IssueID:          issue.ID,
		OriginalFilename: "body.md",
		ByteSize:         42,
		StorageRelPath:   "attachments/bd-1/hash",
	}
	tracker := &attachmentCapableMockTracker{
		mockTracker: newMockTracker("mock"),
		settings:    &AttachmentSettings{Enabled: true, UploadLimit: 1024},
		remote: []TrackerAttachment{{
			ID:       "10001",
			Filename: "body.md",
			ByteSize: 42,
		}},
	}
	engine := NewEngine(tracker, &attachmentListStore{pureTestStore: newPureTestStore(issue), attachments: []*types.Attachment{localAttachment}}, "tester")
	stats := engine.syncPushAttachments(context.Background(), issue, "EXT-1", SyncOptions{PushAttachments: true})
	if stats.skipped != 1 || tracker.uploadCalls != 0 {
		t.Fatalf("stats = %+v uploadCalls=%d", stats, tracker.uploadCalls)
	}
}

func TestPushAttachmentsBypassesBatchPushPath(t *testing.T) {
	tracker := &mockBatchTracker{mockTracker: newMockTracker("mock")}
	store := newPureTestStore(&types.Issue{
		ID:        "bd-1",
		Title:     "Issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	})
	engine := NewEngine(tracker, store, "tester")

	result, err := engine.Sync(context.Background(), SyncOptions{Push: true, PushAttachments: true})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if tracker.batchCalls != 0 {
		t.Fatalf("BatchPush called %d times with PushAttachments enabled", tracker.batchCalls)
	}
	if result.PushStats.Created != 1 {
		t.Fatalf("PushStats = %+v, want one per-issue create", result.PushStats)
	}
}
