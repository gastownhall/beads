package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"
)

// AttachmentSettings is the Jira response from GET /attachment/meta.
type AttachmentSettings struct {
	Enabled     bool  `json:"enabled"`
	UploadLimit int64 `json:"uploadLimit"`
}

// Attachment represents Jira attachment metadata returned in issue fields and
// attachment API responses. The byte content is fetched separately from Content.
type Attachment struct {
	ID        string     `json:"id"`
	Self      string     `json:"self"`
	Filename  string     `json:"filename"`
	Author    *UserField `json:"author"`
	Created   string     `json:"created"`
	Size      int64      `json:"size"`
	MimeType  string     `json:"mimeType"`
	Content   string     `json:"content"`
	Thumbnail string     `json:"thumbnail"`
}

// GetAttachmentSettings returns whether attachments are enabled and the
// instance upload limit advertised by Jira.
func (c *Client) GetAttachmentSettings(ctx context.Context) (*AttachmentSettings, error) {
	apiURL := fmt.Sprintf("%s/attachment/meta", c.apiBase())
	body, err := c.doRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("get attachment settings: %w", err)
	}
	var settings AttachmentSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return nil, fmt.Errorf("parse attachment settings: %w", err)
	}
	return &settings, nil
}

// GetAttachment fetches metadata for one Jira attachment.
func (c *Client) GetAttachment(ctx context.Context, id string) (*Attachment, error) {
	apiURL := fmt.Sprintf("%s/attachment/%s", c.apiBase(), url.PathEscape(id))
	body, err := c.doRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("get attachment %s: %w", id, err)
	}
	var attachment Attachment
	if err := json.Unmarshal(body, &attachment); err != nil {
		return nil, fmt.Errorf("parse attachment %s: %w", id, err)
	}
	return &attachment, nil
}

// DownloadAttachmentContent downloads raw bytes from Jira's attachment content
// endpoint. Jira may redirect this endpoint, which the default HTTP client follows.
func (c *Client) DownloadAttachmentContent(ctx context.Context, id string) ([]byte, error) {
	apiURL := fmt.Sprintf("%s/attachment/content/%s", c.apiBase(), url.PathEscape(id))
	body, err := c.doRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("download attachment %s: %w", id, err)
	}
	return body, nil
}

// UploadAttachment uploads one file to a Jira issue using Jira's documented
// multipart form field name "file" and required X-Atlassian-Token header.
func (c *Client) UploadAttachment(ctx context.Context, issueIDOrKey, filename string, content []byte) ([]Attachment, error) {
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("create attachment form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
		return nil, fmt.Errorf("write attachment form file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close attachment form: %w", err)
	}

	apiURL := fmt.Sprintf("%s/issue/%s/attachments", c.apiBase(), url.PathEscape(issueIDOrKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(requestBody.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("create attachment upload request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "bd-jira-sync/1.0")
	req.Header.Set("X-Atlassian-Token", "no-check")

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload attachment request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read attachment upload response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jira API returned %d: %s", resp.StatusCode, string(body))
	}
	var uploaded []Attachment
	if err := json.Unmarshal(body, &uploaded); err != nil {
		return nil, fmt.Errorf("parse attachment upload response: %w", err)
	}
	return uploaded, nil
}
