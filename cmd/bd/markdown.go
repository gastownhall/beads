// Package main provides the bd command-line interface.
// This file implements markdown file parsing for bulk issue creation from structured markdown documents.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
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
		if p := validation.ParsePriority(content); p != -1 {
			issue.Priority = p
		}
	case "type":
		t, err := validation.ParseIssueType(content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid issue type '%s' in '%s', using default 'task'\n",
				strings.TrimSpace(content), issue.Title)
			issue.IssueType = types.TypeTask
		} else {
			issue.IssueType = t
		}
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
//
//	## Issue Title
//	Description text...
//
//	### Priority
//	2
//
//	### Type
//	feature
//
//	### Description
//	Detailed description...
//
//	### Design
//	Design notes...
//
//	### Acceptance Criteria
//	- Criterion 1
//	- Criterion 2
//
//	### Assignee
//	username
//
//	### Labels
//	label1, label2
//
//	### Dependencies
//	bd-10, bd-20
//
// markdownParseState holds state for parsing markdown files
type markdownParseState struct {
	issues         []*IssueTemplate
	currentIssue   *IssueTemplate
	currentSection string
	sectionContent strings.Builder
}

// finalizeSection processes and resets the current section
func (s *markdownParseState) finalizeSection() {
	if s.currentIssue == nil || s.currentSection == "" {
		return
	}
	content := s.sectionContent.String()
	processIssueSection(s.currentIssue, s.currentSection, content)
	s.sectionContent.Reset()
}

// handleH2Header handles H2 headers (new issue titles)
func (s *markdownParseState) handleH2Header(matches []string) {
	// Finalize previous section if any
	s.finalizeSection()

	// Save previous issue if any
	if s.currentIssue != nil {
		s.issues = append(s.issues, s.currentIssue)
	}

	// Start new issue
	s.currentIssue = &IssueTemplate{
		Title:     strings.TrimSpace(matches[1]),
		Priority:  2,      // Default priority
		IssueType: "task", // Default type
	}
	s.currentSection = ""
}

// handleH3Header handles H3 headers (section titles)
func (s *markdownParseState) handleH3Header(matches []string) {
	// Finalize previous section
	s.finalizeSection()

	// Start new section
	s.currentSection = strings.TrimSpace(matches[1])
}

// handleContentLine handles regular content lines
func (s *markdownParseState) handleContentLine(line string) {
	if s.currentIssue == nil {
		return
	}

	// Content within a section
	if s.currentSection != "" {
		if s.sectionContent.Len() > 0 {
			s.sectionContent.WriteString("\n")
		}
		s.sectionContent.WriteString(line)
		return
	}

	// First lines after title (before any section) become description
	if s.currentIssue.Description == "" && line != "" {
		if s.currentIssue.Description != "" {
			s.currentIssue.Description += "\n"
		}
		s.currentIssue.Description += line
	}
}

// finalize completes parsing and returns the results
func (s *markdownParseState) finalize() ([]*IssueTemplate, error) {
	// Finalize last section and issue
	s.finalizeSection()
	if s.currentIssue != nil {
		s.issues = append(s.issues, s.currentIssue)
	}

	// Check if we found any issues
	if len(s.issues) == 0 {
		return nil, fmt.Errorf("no issues found in markdown file (expected ## Issue Title format)")
	}

	return s.issues, nil
}

// createMarkdownScanner creates a scanner with appropriate buffer size
func createMarkdownScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large markdown files
	const maxScannerBuffer = 1024 * 1024 // 1MB
	buf := make([]byte, maxScannerBuffer)
	scanner.Buffer(buf, maxScannerBuffer)
	return scanner
}

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

	state := &markdownParseState{}
	scanner := createMarkdownScanner(file)

	for scanner.Scan() {
		line := scanner.Text()

		// Check for H2 (new issue)
		if matches := h2Regex.FindStringSubmatch(line); matches != nil {
			state.handleH2Header(matches)
			continue
		}

		// Check for H3 (section within issue)
		if matches := h3Regex.FindStringSubmatch(line); matches != nil {
			state.handleH3Header(matches)
			continue
		}

		// Regular content line
		state.handleContentLine(line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	return state.finalize()
}

// createIssuesFromMarkdown parses a markdown file and creates multiple issues from it
func createIssuesFromMarkdown(_ *cobra.Command, filepath string) {
	// Parse markdown file first (doesn't require store access)
	templates, err := parseMarkdownFile(filepath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing markdown file: %v\n", err)
		os.Exit(1)
	}

	if len(templates) == 0 {
		fmt.Fprintf(os.Stderr, "No issues found in markdown file\n")
		os.Exit(1)
	}

	// If daemon is running, use RPC batch create (GH#719)
	if daemonClient != nil {
		createIssuesFromMarkdownViaDaemon(templates, filepath)
		return
	}

	// Direct mode: ensure globals are initialized
	if store == nil {
		fmt.Fprintf(os.Stderr, "Error: database not initialized\n")
		os.Exit(1)
	}
	if actor == "" {
		actor = "bd" // Default actor if not set
	}

	ctx := rootCtx
	createdIssues := []*types.Issue{}
	failedIssues := []string{}

	// Create each issue
	for _, template := range templates {
		issue := &types.Issue{
			Title:              template.Title,
			Description:        template.Description,
			Design:             template.Design,
			AcceptanceCriteria: template.AcceptanceCriteria,
			Status:             types.StatusOpen,
			Priority:           template.Priority,
			IssueType:          template.IssueType,
			Assignee:           template.Assignee,
		}

		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating issue '%s': %v\n", template.Title, err)
			failedIssues = append(failedIssues, template.Title)
			continue
		}

		// Add labels
		for _, label := range template.Labels {
			if err := store.AddLabel(ctx, issue.ID, label, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, issue.ID, err)
			}
		}

		// Add dependencies
		for _, depSpec := range template.Dependencies {
			depSpec = strings.TrimSpace(depSpec)
			if depSpec == "" {
				continue
			}

			var depType types.DependencyType
			var dependsOnID string

			// Parse format: "type:id" or just "id" (defaults to "blocks")
			if strings.Contains(depSpec, ":") {
				parts := strings.SplitN(depSpec, ":", 2)
				if len(parts) != 2 {
					fmt.Fprintf(os.Stderr, "Warning: invalid dependency format '%s' for %s\n", depSpec, issue.ID)
					continue
				}
				depType = types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID = strings.TrimSpace(parts[1])
			} else {
				depType = types.DepBlocks
				dependsOnID = depSpec
			}

			if !depType.IsValid() {
				fmt.Fprintf(os.Stderr, "Warning: invalid dependency type '%s' for %s\n", depType, issue.ID)
				continue
			}

			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: dependsOnID,
				Type:        depType,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", issue.ID, dependsOnID, err)
			}
		}

		createdIssues = append(createdIssues, issue)
	}

	// Report failures if any
	if len(failedIssues) > 0 {
		fmt.Fprintf(os.Stderr, "\n%s Failed to create %d issues:\n", ui.RenderFail("✗"), len(failedIssues))
		for _, title := range failedIssues {
			fmt.Fprintf(os.Stderr, "  - %s\n", title)
		}
	}

	if jsonOutput {
		outputJSON(createdIssues)
	} else {
		fmt.Printf("%s Created %d issues from %s:\n", ui.RenderPass("✓"), len(createdIssues), filepath)
		for _, issue := range createdIssues {
			fmt.Printf("  %s: %s [P%d, %s]\n", issue.ID, issue.Title, issue.Priority, issue.IssueType)
		}
	}
}

// createIssuesFromMarkdownViaDaemon creates issues via daemon RPC batch operation
func createIssuesFromMarkdownViaDaemon(templates []*IssueTemplate, filepath string) {
	createdIssues := []*types.Issue{}
	failedIssues := []string{}

	// Build batch operations for all issues
	operations := make([]rpc.BatchOperation, 0, len(templates))
	for _, template := range templates {
		createArgs := &rpc.CreateArgs{
			Title:              template.Title,
			Description:        template.Description,
			Design:             template.Design,
			AcceptanceCriteria: template.AcceptanceCriteria,
			IssueType:          string(template.IssueType),
			Priority:           template.Priority,
			Assignee:           template.Assignee,
			Labels:             template.Labels,
			Dependencies:       template.Dependencies,
		}

		argsJSON, err := json.Marshal(createArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling args for '%s': %v\n", template.Title, err)
			failedIssues = append(failedIssues, template.Title)
			continue
		}

		operations = append(operations, rpc.BatchOperation{
			Operation: "create",
			Args:      argsJSON,
		})
	}

	// Execute batch
	batchArgs := &rpc.BatchArgs{Operations: operations}
	resp, err := daemonClient.Batch(batchArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing batch create: %v\n", err)
		os.Exit(1)
	}

	// Parse batch response
	var batchResp rpc.BatchResponse
	if err := json.Unmarshal(resp.Data, &batchResp); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing batch response: %v\n", err)
		os.Exit(1)
	}

	// Process results
	for i, result := range batchResp.Results {
		if i >= len(templates) {
			break
		}
		template := templates[i]

		if !result.Success {
			fmt.Fprintf(os.Stderr, "Error creating issue '%s': %s\n", template.Title, result.Error)
			failedIssues = append(failedIssues, template.Title)
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal(result.Data, &issue); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: created issue '%s' but failed to parse response: %v\n", template.Title, err)
			// Still count as success since the issue was created
			createdIssues = append(createdIssues, &types.Issue{Title: template.Title})
			continue
		}

		// Run create hook for each issue
		if hookRunner != nil {
			hookRunner.Run(hooks.EventCreate, &issue)
		}

		createdIssues = append(createdIssues, &issue)
	}

	// Report failures if any
	if len(failedIssues) > 0 {
		fmt.Fprintf(os.Stderr, "\n%s Failed to create %d issues:\n", ui.RenderFail("✗"), len(failedIssues))
		for _, title := range failedIssues {
			fmt.Fprintf(os.Stderr, "  - %s\n", title)
		}
	}

	if jsonOutput {
		outputJSON(createdIssues)
	} else {
		fmt.Printf("%s Created %d issues from %s:\n", ui.RenderPass("✓"), len(createdIssues), filepath)
		for _, issue := range createdIssues {
			fmt.Printf("  %s: %s [P%d, %s]\n", issue.ID, issue.Title, issue.Priority, issue.IssueType)
		}
	}
}
