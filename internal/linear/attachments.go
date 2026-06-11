package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const fileUploadCacheControl = "public, max-age=31536000"

// CreateAttachment creates or updates a Linear issue attachment URL card.
// Linear uses the pair (issueId, url) as an idempotency key, so retrying the
// same URL on the same issue updates the existing attachment instead of creating
// a duplicate card.
func (c *Client) CreateAttachment(ctx context.Context, issueID, title, subtitle, attachmentURL string, metadata map[string]interface{}) (*Attachment, error) {
	query := `
		mutation CreateAttachment($input: AttachmentCreateInput!) {
			attachmentCreate(input: $input) {
				success
				attachment {
					id
					title
					subtitle
					url
					iconUrl
					metadata
					createdAt
					updatedAt
				}
			}
		}
	`
	input := map[string]interface{}{
		"issueId": issueID,
		"title":   title,
		"url":     attachmentURL,
	}
	if subtitle != "" {
		input["subtitle"] = subtitle
	}
	if len(metadata) > 0 {
		input["metadata"] = metadata
	}
	data, err := c.Execute(ctx, &GraphQLRequest{
		Query:     query,
		Variables: map[string]interface{}{"input": input},
	})
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}
	var resp AttachmentCreateResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse attachmentCreate response: %w", err)
	}
	if !resp.AttachmentCreate.Success {
		return nil, fmt.Errorf("attachmentCreate unsuccessful")
	}
	return &resp.AttachmentCreate.Attachment, nil
}

// AttachmentsForURL queries Linear's URL-idempotent attachment lookup.
func (c *Client) AttachmentsForURL(ctx context.Context, attachmentURL string) ([]Attachment, error) {
	query := `
		query AttachmentsForURL($url: String!) {
			attachmentsForURL(url: $url) {
				nodes {
					id
					title
					subtitle
					url
					iconUrl
					metadata
					createdAt
					updatedAt
				}
			}
		}
	`
	data, err := c.Execute(ctx, &GraphQLRequest{
		Query:     query,
		Variables: map[string]interface{}{"url": attachmentURL},
	})
	if err != nil {
		return nil, fmt.Errorf("query attachmentsForURL: %w", err)
	}
	var resp AttachmentsForURLResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse attachmentsForURL response: %w", err)
	}
	return resp.AttachmentsForURL.Nodes, nil
}

// RequestFileUpload asks Linear for a pre-signed file upload target.
func (c *Client) RequestFileUpload(ctx context.Context, contentType, filename string, size int64) (*UploadFile, error) {
	query := `
		mutation FileUpload($contentType: String!, $filename: String!, $size: Int!) {
			fileUpload(contentType: $contentType, filename: $filename, size: $size) {
				success
				uploadFile {
					uploadUrl
					assetUrl
					headers {
						key
						value
					}
				}
			}
		}
	`
	data, err := c.Execute(ctx, &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"contentType": contentType,
			"filename":    filename,
			"size":        size,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("request fileUpload: %w", err)
	}
	var resp FileUploadResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse fileUpload response: %w", err)
	}
	if !resp.FileUpload.Success {
		return nil, fmt.Errorf("fileUpload unsuccessful")
	}
	if strings.TrimSpace(resp.FileUpload.UploadFile.UploadURL) == "" || strings.TrimSpace(resp.FileUpload.UploadFile.AssetURL) == "" {
		return nil, fmt.Errorf("fileUpload response missing uploadUrl or assetUrl")
	}
	return &resp.FileUpload.UploadFile, nil
}

// UploadFileToLinear performs Linear's documented two-step upload: request a
// signed upload URL, then PUT the bytes with Content-Type, Cache-Control, and
// every header returned by fileUpload. The returned URL points to Linear private
// cloud storage and should be linked from an issue attachment.
func (c *Client) UploadFileToLinear(ctx context.Context, contentType, filename string, content []byte) (string, error) {
	upload, err := c.RequestFileUpload(ctx, contentType, filename, int64(len(content)))
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upload.UploadURL, bytes.NewReader(content))
	if err != nil {
		return "", fmt.Errorf("create signed PUT request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Cache-Control", fileUploadCacheControl)
	for _, header := range upload.Headers {
		if strings.TrimSpace(header.Key) != "" {
			req.Header.Set(header.Key, header.Value)
		}
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("signed PUT upload: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if err != nil {
		return "", fmt.Errorf("read signed PUT response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("signed PUT upload returned %d: %s", resp.StatusCode, string(body))
	}
	return upload.AssetURL, nil
}

// DownloadFile downloads a Linear file-storage URL. Linear's file storage docs
// require the normal GraphQL Authorization header for private uploads; the same
// header is therefore attached to upload URLs, and harmless for test/external URLs
// that ignore it.
func (c *Client) DownloadFile(ctx context.Context, fileURL string) ([]byte, error) {
	if _, err := url.ParseRequestURI(fileURL); err != nil {
		return nil, fmt.Errorf("invalid file URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create file download request: %w", err)
	}
	authValue, err := c.authHeader()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authValue)
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read file download response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("file download returned %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
