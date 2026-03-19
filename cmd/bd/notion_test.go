package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/notion"
)

type fakeNotionStatusService struct {
	resp *notion.StatusResponse
	err  error
	req  notion.StatusRequest
}

func (f *fakeNotionStatusService) Status(_ context.Context, req notion.StatusRequest) (*notion.StatusResponse, error) {
	f.req = req
	return f.resp, f.err
}

func TestNotionStatusFlagsRegistered(t *testing.T) {
	t.Parallel()

	if notionStatusCmd.Flags().Lookup("ncli-bin") == nil {
		t.Fatal("missing --ncli-bin")
	}
	if notionStatusCmd.Flags().Lookup("database-id") == nil {
		t.Fatal("missing --database-id")
	}
	if notionStatusCmd.Flags().Lookup("view-url") == nil {
		t.Fatal("missing --view-url")
	}
}

func TestRunNotionStatusPassesFlagsToClient(t *testing.T) {
	t.Parallel()

	originalFactory := newNotionStatusClient
	originalJSON := jsonOutput
	originalBin := notionNCLIBin
	originalDB := notionDatabaseID
	originalView := notionViewURL
	t.Cleanup(func() {
		newNotionStatusClient = originalFactory
		jsonOutput = originalJSON
		notionNCLIBin = originalBin
		notionDatabaseID = originalDB
		notionViewURL = originalView
	})

	fake := &fakeNotionStatusService{
		resp: &notion.StatusResponse{Ready: true},
	}
	newNotionStatusClient = func(binaryPath string) notionStatusClient {
		if binaryPath != "/tmp/ncli" {
			t.Fatalf("binary path = %q, want /tmp/ncli", binaryPath)
		}
		return fake
	}
	jsonOutput = false
	notionNCLIBin = "/tmp/ncli"
	notionDatabaseID = "db_123"
	notionViewURL = "https://example.com/view"

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())

	if err := runNotionStatus(cmd, nil); err != nil {
		t.Fatalf("runNotionStatus returned error: %v", err)
	}
	if fake.req.DatabaseID != "db_123" {
		t.Fatalf("database id = %q", fake.req.DatabaseID)
	}
	if fake.req.ViewURL != "https://example.com/view" {
		t.Fatalf("view url = %q", fake.req.ViewURL)
	}
	if !strings.Contains(stdout.String(), "Ready: yes") {
		t.Fatalf("stdout = %q, want Ready: yes", stdout.String())
	}
}

func TestRunNotionStatusReturnsClientError(t *testing.T) {
	t.Parallel()

	originalFactory := newNotionStatusClient
	t.Cleanup(func() { newNotionStatusClient = originalFactory })

	wantErr := errors.New("ncli missing")
	newNotionStatusClient = func(string) notionStatusClient {
		return &fakeNotionStatusService{err: wantErr}
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runNotionStatus(cmd, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestRenderNotionStatusIncludesArchiveWarning(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	renderNotionStatus(cmd, &notion.StatusResponse{
		Ready:        true,
		DataSourceID: "ds_123",
		Archive: &notion.ArchiveCapability{
			Supported: false,
			Reason:    "live MCP does not expose archive",
		},
	})

	output := stdout.String()
	if !strings.Contains(output, "Archive Support: unavailable") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "Archive Reason: live MCP does not expose archive") {
		t.Fatalf("output = %q", output)
	}
}
