package ado

import (
	"bytes"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/renderer/html"
)

// HTMLToMarkdown converts ADO HTML description to Markdown for beads storage.
// It sanitizes the HTML first via bluemonday to strip dangerous elements
// (script tags, event handlers), then converts clean HTML to Markdown.
// Returns empty string for empty input.
func HTMLToMarkdown(rawHTML string) (string, error) {
	if strings.TrimSpace(rawHTML) == "" {
		return "", nil
	}

	p := bluemonday.UGCPolicy()
	sanitized := p.Sanitize(rawHTML)

	if strings.TrimSpace(sanitized) == "" {
		return "", nil
	}

	md, err := htmltomarkdown.ConvertString(sanitized)
	if err != nil {
		return "", err
	}

	return strings.TrimRight(md, " \t\r\n"), nil
}

// MarkdownToHTML converts beads Markdown description to HTML for ADO storage.
// Uses goldmark with safe renderer settings (no raw HTML passthrough).
// Returns empty string for empty input.
func MarkdownToHTML(md string) (string, error) {
	if strings.TrimSpace(md) == "" {
		return "", nil
	}

	renderer := goldmark.New(
		goldmark.WithRendererOptions(
			html.WithXHTML(),
		),
	)

	var buf bytes.Buffer
	if err := renderer.Convert([]byte(md), &buf); err != nil {
		return "", err
	}

	return strings.TrimRight(buf.String(), " \t\r\n"), nil
}
