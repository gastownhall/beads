package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttachmentSettingsAndDownloadUseJiraAPIPaths(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/attachment/meta":
			_ = json.NewEncoder(w).Encode(AttachmentSettings{Enabled: true, UploadLimit: 1024})
		case "/rest/api/3/attachment/content/10001":
			_, _ = w.Write([]byte("jira file"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "3")
	settings, err := c.GetAttachmentSettings(context.Background())
	if err != nil {
		t.Fatalf("GetAttachmentSettings() error = %v", err)
	}
	if !settings.Enabled || settings.UploadLimit != 1024 {
		t.Fatalf("settings = %+v", settings)
	}
	data, err := c.DownloadAttachmentContent(context.Background(), "10001")
	if err != nil {
		t.Fatalf("DownloadAttachmentContent() error = %v", err)
	}
	if string(data) != "jira file" {
		t.Fatalf("download = %q", data)
	}
	if strings.Join(paths, ",") != "/rest/api/3/attachment/meta,/rest/api/3/attachment/content/10001" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestUploadAttachmentUsesJiraMultipartContract(t *testing.T) {
	var gotPath, gotToken, gotFilename string
	var gotBody []byte
	var gotMultipart bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Atlassian-Token")
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader() error = %v", err)
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart() error = %v", err)
			}
			if part.FormName() == "file" {
				gotMultipart = true
				gotFilename = part.FileName()
				gotBody, _ = io.ReadAll(part)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Attachment{{
			ID:       "10001",
			Filename: gotFilename,
			Size:     int64(len(gotBody)),
			MimeType: "text/plain",
			Content:  srvURL(r) + "/rest/api/3/attachment/content/10001",
		}})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "3")
	uploaded, err := c.UploadAttachment(context.Background(), "PROJ-1", "body.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("UploadAttachment() error = %v", err)
	}
	if gotPath != "/rest/api/3/issue/PROJ-1/attachments" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotToken != "no-check" {
		t.Fatalf("X-Atlassian-Token = %q", gotToken)
	}
	if !gotMultipart || gotFilename != "body.txt" || string(gotBody) != "hello" {
		t.Fatalf("multipart file = %v %q %q", gotMultipart, gotFilename, gotBody)
	}
	if len(uploaded) != 1 || uploaded[0].ID != "10001" {
		t.Fatalf("uploaded = %+v", uploaded)
	}
}

func TestUploadAttachmentRejectsMissingFilePartInMock(t *testing.T) {
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	_ = writer.WriteField("wrong", "value")
	_ = writer.Close()
	req, err := http.NewRequest(http.MethodPost, "http://example.test", &requestBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	reader, err := req.MultipartReader()
	if err != nil {
		t.Fatalf("MultipartReader() error = %v", err)
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("NextPart() error = %v", err)
		}
		if part.FormName() == "file" {
			t.Fatal("unexpected file field")
		}
	}
}

func TestJiraAttachmentMapping(t *testing.T) {
	issue := &Issue{
		ID:  "10000",
		Key: "PROJ-1",
		Fields: IssueFields{
			Summary: "Bug",
			Attachments: []Attachment{{
				ID:        "10001",
				Self:      "https://jira/rest/api/3/attachment/10001",
				Filename:  "body.md",
				MimeType:  "text/markdown",
				Size:      42,
				Content:   "https://jira/rest/api/3/attachment/content/10001",
				Thumbnail: "https://jira/rest/api/3/attachment/thumbnail/10001",
				Author:    &UserField{AccountID: "abc", DisplayName: "Ada"},
				Created:   "2026-06-07T12:00:00.000+0000",
			}},
		},
	}
	ti := jiraToTrackerIssue(issue, nil)
	if len(ti.Attachments) != 1 {
		t.Fatalf("attachments = %+v", ti.Attachments)
	}
	got := ti.Attachments[0]
	if got.ID != "10001" || got.Filename != "body.md" || got.MimeType != "text/markdown" || got.ByteSize != 42 {
		t.Fatalf("mapped attachment = %+v", got)
	}
	if got.ContentURL == "" || got.ThumbnailURL == "" || got.AuthorID != "abc" || got.AuthorName != "Ada" {
		t.Fatalf("mapped attachment details = %+v", got)
	}
}

func srvURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
