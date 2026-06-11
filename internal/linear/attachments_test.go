package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestUploadFileToLinearUsesFileUploadAndSignedPUTHeaders(t *testing.T) {
	var putPath, putContentType, putCacheControl, putToken string
	var putBody []byte
	uploadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		putPath = r.URL.Path
		putContentType = r.Header.Get("Content-Type")
		putCacheControl = r.Header.Get("Cache-Control")
		putToken = r.Header.Get("x-linear-upload-token")
		putBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadSrv.Close()

	var sawFileUpload bool
	graphQLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		if !strings.Contains(req.Query, "fileUpload") {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		sawFileUpload = true
		if req.Variables["contentType"] != "text/markdown" || req.Variables["filename"] != "body.md" {
			t.Fatalf("fileUpload variables = %#v", req.Variables)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"fileUpload": map[string]interface{}{
					"success": true,
					"uploadFile": map[string]interface{}{
						"uploadUrl": uploadSrv.URL + "/signed",
						"assetUrl":  "https://uploads.linear.app/workspace/body-md",
						"headers": []map[string]string{{
							"key":   "x-linear-upload-token",
							"value": "abc123",
						}},
					},
				},
			},
		})
	}))
	defer graphQLSrv.Close()

	client := NewClient("lin_api_key", "team").WithEndpoint(graphQLSrv.URL)
	assetURL, err := client.UploadFileToLinear(context.Background(), "text/markdown", "body.md", []byte("# Body\n"))
	if err != nil {
		t.Fatalf("UploadFileToLinear() error = %v", err)
	}
	if !sawFileUpload {
		t.Fatal("fileUpload mutation was not called")
	}
	if assetURL != "https://uploads.linear.app/workspace/body-md" {
		t.Fatalf("assetURL = %q", assetURL)
	}
	if putPath != "/signed" || putContentType != "text/markdown" || putCacheControl != fileUploadCacheControl || putToken != "abc123" {
		t.Fatalf("PUT request path=%q contentType=%q cacheControl=%q token=%q", putPath, putContentType, putCacheControl, putToken)
	}
	if !bytes.Equal(putBody, []byte("# Body\n")) {
		t.Fatalf("PUT body = %q", putBody)
	}
}

func TestCreateAttachmentAndAttachmentsForURLUseLinearURLCardContract(t *testing.T) {
	var sawCreate, sawLookup bool
	graphQLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "attachmentCreate"):
			sawCreate = true
			input := req.Variables["input"].(map[string]interface{})
			if input["issueId"] != "issue-uuid" || input["url"] != "https://uploads.linear.app/file" || input["title"] != "body.md" {
				t.Fatalf("attachmentCreate input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"attachmentCreate": map[string]interface{}{
						"success": true,
						"attachment": map[string]interface{}{
							"id":       "att-1",
							"title":    "body.md",
							"subtitle": "text/markdown",
							"url":      "https://uploads.linear.app/file",
							"metadata": map[string]interface{}{"filename": "body.md", "byte_size": 7},
						},
					},
				},
			})
		case strings.Contains(req.Query, "attachmentsForURL"):
			sawLookup = true
			if req.Variables["url"] != "https://uploads.linear.app/file" {
				t.Fatalf("attachmentsForURL variables = %#v", req.Variables)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"attachmentsForURL": map[string]interface{}{
						"nodes": []map[string]interface{}{{
							"id":    "att-1",
							"title": "body.md",
							"url":   "https://uploads.linear.app/file",
						}},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer graphQLSrv.Close()

	client := NewClient("lin_api_key", "team").WithEndpoint(graphQLSrv.URL)
	created, err := client.CreateAttachment(context.Background(), "issue-uuid", "body.md", "text/markdown", "https://uploads.linear.app/file", map[string]interface{}{"filename": "body.md", "byte_size": 7})
	if err != nil {
		t.Fatalf("CreateAttachment() error = %v", err)
	}
	found, err := client.AttachmentsForURL(context.Background(), created.URL)
	if err != nil {
		t.Fatalf("AttachmentsForURL() error = %v", err)
	}
	if !sawCreate || !sawLookup || len(found) != 1 || found[0].ID != "att-1" {
		t.Fatalf("sawCreate=%v sawLookup=%v found=%+v", sawCreate, sawLookup, found)
	}
}

func TestLinearAttachmentMappingUsesMetadataForFileIdentity(t *testing.T) {
	issue := &Issue{
		ID:         "issue-uuid",
		Identifier: "ENG-1",
		Title:      "Issue",
		Attachments: &Attachments{Nodes: []Attachment{{
			ID:       "att-1",
			Title:    "Attachment card",
			URL:      "https://uploads.linear.app/file",
			Metadata: map[string]interface{}{"filename": "body.md", "mime_type": "text/markdown", "byte_size": float64(7)},
		}}},
	}
	ti := linearToTrackerIssue(issue)
	if len(ti.Attachments) != 1 {
		t.Fatalf("attachments = %+v", ti.Attachments)
	}
	got := ti.Attachments[0]
	if got.ID != "att-1" || got.Filename != "body.md" || got.MimeType != "text/markdown" || got.ByteSize != 7 || got.ContentURL == "" {
		t.Fatalf("mapped attachment = %+v", got)
	}
}

func TestTrackerUploadAttachmentUsesFileUploadThenAttachmentCreate(t *testing.T) {
	var putCalled, createCalled bool
	uploadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		putCalled = true
		if r.Method != http.MethodPut || r.Header.Get("x-linear-upload-token") != "abc123" {
			t.Fatalf("PUT method/header = %s %q", r.Method, r.Header.Get("x-linear-upload-token"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadSrv.Close()

	graphQLSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "IssueByIdentifier"):
			_ = json.NewEncoder(w).Encode(issueByIdentifierGraphQL("issue-uuid", "ENG-1", nil))
		case strings.Contains(req.Query, "fileUpload"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"fileUpload": map[string]interface{}{
						"success": true,
						"uploadFile": map[string]interface{}{
							"uploadUrl": uploadSrv.URL + "/signed",
							"assetUrl":  "https://uploads.linear.app/workspace/body-md",
							"headers":   []map[string]string{{"key": "x-linear-upload-token", "value": "abc123"}},
						},
					},
				},
			})
		case strings.Contains(req.Query, "attachmentCreate"):
			createCalled = true
			input := req.Variables["input"].(map[string]interface{})
			if input["issueId"] != "issue-uuid" || input["url"] != "https://uploads.linear.app/workspace/body-md" {
				t.Fatalf("attachmentCreate input = %#v", input)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"attachmentCreate": map[string]interface{}{
						"success": true,
						"attachment": map[string]interface{}{
							"id":       "att-1",
							"title":    "body.md",
							"subtitle": "text/markdown",
							"url":      "https://uploads.linear.app/workspace/body-md",
							"metadata": input["metadata"],
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer graphQLSrv.Close()

	tr := &Tracker{
		clients: map[string]*Client{"team": NewClient("lin_api_key", "team").WithEndpoint(graphQLSrv.URL)},
		teamIDs: []string{"team"},
	}
	uploaded, err := tr.UploadAttachment(context.Background(), "ENG-1", &types.Attachment{
		IssueID:          "bd-1",
		OriginalFilename: "body.md",
		MimeType:         "text/markdown",
		ByteSize:         7,
		HashAlgorithm:    "sha256",
		ContentHash:      "abc",
	}, []byte("# Body\n"))
	if err != nil {
		t.Fatalf("UploadAttachment() error = %v", err)
	}
	if !putCalled || !createCalled {
		t.Fatalf("putCalled=%v createCalled=%v", putCalled, createCalled)
	}
	if uploaded.ID != "att-1" || uploaded.ContentURL == "" || uploaded.Filename != "body.md" {
		t.Fatalf("uploaded = %+v", uploaded)
	}
}

func issueByIdentifierGraphQL(id, identifier string, attachments []map[string]interface{}) map[string]interface{} {
	if attachments == nil {
		attachments = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []map[string]interface{}{{
					"id":          id,
					"identifier":  identifier,
					"title":       "Issue",
					"description": "",
					"url":         "https://linear.app/issue/" + identifier,
					"priority":    2,
					"createdAt":   "2026-06-07T12:00:00Z",
					"updatedAt":   "2026-06-07T12:00:00Z",
					"attachments": map[string]interface{}{"nodes": attachments},
				}},
			},
		},
	}
}

var _ tracker.AttachmentSyncTracker = (*Tracker)(nil)
