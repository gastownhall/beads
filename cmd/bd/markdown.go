// Package main provides the bd command-line interface.
// This file implements markdown file parsing for bulk issue creation from structured markdown documents.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

var (
	// h2Regex matches markdown H2 headers (## Title) for issue titles.
	// Compiled once at package init for performance.
	h2Regex = regexp.MustCompile(`^##\s+(.+)$`)

	// h3Regex matches markdown H3 headers (### Section) for issue sections.
	// Compiled once at package init for performance.
	h3Regex = regexp.MustCompile(`^###\s+(.+)$`)
)

// IssueTemplate represents a parsed issue from markdown
type IssueTemplate struct {
	Title              string
	Description        string
	Design             string
	AcceptanceCriteria string
	Priority           int
	IssueType          types.IssueType
	Assignee           string
	Labels             []string
	Dependencies       []string
}

// parsePriority extracts and validates a priority value from content.
// Returns the parsed priority (0-4) or -1 if invalid.
func parsePriority(content string) int {
	var p int
	if _, err := fmt.Sscanf(content, "%d", &p); err == nil && p >= 0 && p <= 4 {
		return p
	}
	return -1 // Invalid
}

// parseIssueType extracts and validates an issue type from content.
// Returns the validated type or empty string if invalid.
func parseIssueType(content, issueTitle string) types.IssueType {
	issueType := types.IssueType(strings.TrimSpace(content))

	// Validate issue type
	validTypes := map[types.IssueType]bool{
		types.TypeBug:     true,
		types.TypeFeature: true,
		types.TypeTask:    true,
		types.TypeEpic:    true,
		types.TypeChore:   true,
	}

	if !validTypes[issueType] {
		// Warn but continue with default
		fmt.Fprintf(os.Stderr, "Warning: invalid issue type '%s' in '%s', using default 'task'\n",
			issueType, issueTitle)
		return types.TypeTask
	}

	return issueType
}

// parseStringList extracts a list of strings from content, splitting by comma or whitespace.
// This is a generic helper used by parseLabels and parseDependencies.
func parseStringList(content string) []string {
	var items []string
	fields := strings.FieldsFunc(content, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n'
	})
	for _, item := range fields {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

// parseLabels extracts labels from content, splitting by comma or whitespace.
func parseLabels(content string) []string {
	return parseStringList(content)
}

// parseDependencies extracts dependencies from content, splitting by comma or whitespace.
func parseDependencies(content string) []string {
	return parseStringList(content)
}

// processIssueSection processes a parsed section and updates the issue template.
func processIssueSection(issue *IssueTemplate, section, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}

	switch strings.ToLower(section) {
	case "priority":
		if p := parsePriority(content); p != -1 {
			issue.Priority = p
		}
	case "type":
		issue.IssueType = parseIssueType(content, issue.Title)
	case "description":
		issue.Description = content
	case "design":
		issue.Design = content
	case "acceptance criteria", "acceptance":
		issue.AcceptanceCriteria = content
	case "assignee":
		issue.Assignee = strings.TrimSpace(content)
	case "labels":
		issue.Labels = parseLabels(content)
	case "dependencies", "deps":
		issue.Dependencies = parseDependencies(content)
	}
}

// validateMarkdownPath validates and cleans a markdown file path to prevent security issues.
// It checks for directory traversal attempts and ensures the file is a markdown file.
func validateMarkdownPath(path string) (string, error) {
	// Clean the path
	cleanPath := filepath.Clean(path)

	// Prevent directory traversal
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("invalid file path: directory traversal not allowed")
	}

	// Ensure it's a markdown file
	ext := strings.ToLower(filepath.Ext(cleanPath))
	if ext != ".md" && ext != ".markdown" {
		return "", fmt.Errorf("invalid file type: only .md and .markdown files are supported")
	}

	// Check file exists and is not a directory
	info, err := os.Stat(cleanPath)
	if err != nil {
		return "", fmt.Errorf("cannot access file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file")
	}

	return cleanPath, nil
}

// parseMarkdownFile parses a markdown file and extracts issue templates.
// Expected format:
//   ## Issue Title
//   Description text...
//
//   ### Priority
//   2
//
//   ### Type
//   feature
//
//   ### Description
//   Detailed description...
//
//   ### Design
//   Design notes...
//
//   ### Acceptance Criteria
//   - Criterion 1
//   - Criterion 2
//
//   ### Assignee
//   username
//
//   ### Labels
//   label1, label2
//
//   ### Dependencies
//   bd-10, bd-20
func parseMarkdownFile(path string) ([]*IssueTemplate, error) {
	// Validate and clean the file path
	cleanPath, err := validateMarkdownPath(path)
	if err != nil {
		return nil, err
	}

	// #nosec G304 -- Path is validated by validateMarkdownPath which prevents traversal
	file, err := os.Open(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer func() {
		_ = file.Close() // Close errors on read-only operations are not actionable
	}()

	var issues []*IssueTemplate
	var currentIssue *IssueTemplate
	var currentSection string
	var sectionContent strings.Builder

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large markdown files
	const maxScannerBuffer = 1024 * 1024 // 1MB
	buf := make([]byte, maxScannerBuffer)
	scanner.Buffer(buf, maxScannerBuffer)

	// Helper to finalize current section
	finalizeSection := func() {
		if currentIssue == nil || currentSection == "" {
			return
		}
		content := sectionContent.String()
		processIssueSection(currentIssue, currentSection, content)
		sectionContent.Reset()
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Check for H2 (new issue)
		if matches := h2Regex.FindStringSubmatch(line); matches != nil {
			// Finalize previous section if any
			finalizeSection()

			// Save previous issue if any
			if currentIssue != nil {
				issues = append(issues, currentIssue)
			}

			// Start new issue
			currentIssue = &IssueTemplate{
				Title:     strings.TrimSpace(matches[1]),
				Priority:  2,        // Default priority
				IssueType: "task",   // Default type
			}
			currentSection = ""
			continue
		}

		// Check for H3 (section within issue)
		if matches := h3Regex.FindStringSubmatch(line); matches != nil {
			// Finalize previous section
			finalizeSection()

			// Start new section
			currentSection = strings.TrimSpace(matches[1])
			continue
		}

		// Regular content line - append to current section
		if currentIssue != nil && currentSection != "" {
			if sectionContent.Len() > 0 {
				sectionContent.WriteString("\n")
			}
			sectionContent.WriteString(line)
		} else if currentIssue != nil && currentSection == "" && currentIssue.Description == "" {
			// First lines after title (before any section) become description
			if line != "" {
				if currentIssue.Description != "" {
					currentIssue.Description += "\n"
				}
				currentIssue.Description += line
			}
		}
	}

	// Finalize last section and issue
	finalizeSection()
	if currentIssue != nil {
		issues = append(issues, currentIssue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	// Check if we found any issues
	if len(issues) == 0 {
		return nil, fmt.Errorf("no issues found in markdown file (expected ## Issue Title format)")
	}

	return issues, nil
}
