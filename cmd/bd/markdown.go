// Package main provides the bd command-line interface.
// This file implements markdown file parsing for bulk issue creation from structured markdown documents.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/issueops"
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

	// Lines after title (before any section) become description
	if line != "" {
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

// createIssuesFromMarkdown creates every issue in a markdown file as ONE act,
// through issueops.BatchCreator. It parses the file, lints it, builds one
// request and prints what came back; the proxied route builds the SAME request.
func createIssuesFromMarkdown(ctx context.Context, in createInput) error {
	templates, err := parseMarkdownFile(in.markdownFile)
	if err != nil {
		return HandleError("parsing markdown file: %v", err)
	}
	if len(templates) == 0 {
		return HandleError("no issues found in markdown file")
	}
	if store == nil {
		return HandleErrorWithHint("database not initialized", diagHint())
	}
	request, err := buildMarkdownBatchRequest(templates, in)
	if err != nil {
		return err
	}
	// The role creates its Dolt version commit inside the storage layer, so
	// `--dolt-auto-commit batch` can only defer it by saying so on the context.
	// commitPendingIfEmbedded below is the OTHER half and cannot substitute: it
	// correctly no-ops in batch mode, which is exactly why forgetting this line
	// produces a per-write commit that nothing later suppresses.
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	creator, err := store.BatchCreator()
	if err != nil {
		return HandleError("%v", err)
	}
	result, err := creator.CreateBatch(opsCtx, request)
	if err != nil {
		return HandleError("creating issues from markdown: %v", err)
	}
	issueIDs := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		issueIDs = append(issueIDs, issue.ID)
	}
	if err := commitPendingIfEmbedded(ctx, store, request.Actor, doltAutoCommitParams{
		Command:         "create",
		IssueIDs:        issueIDs,
		MessageOverride: request.Provenance,
	}); err != nil {
		WarnError("failed to commit: %v", err)
	}
	return reportMarkdownBatch(result.Issues, in)
}

// buildMarkdownBatchRequest is the ONE projection of a parsed markdown file
// onto the role's request, shared by both front doors, so the two routes cannot
// answer differently.
//
// It lints first, because the lint is about the FILE the user wrote and
// refusing it here costs no transaction.
func buildMarkdownBatchRequest(templates []*IssueTemplate, in createInput) (issueops.CreateBatchRequest, error) {
	if err := lintMarkdownTemplates(templates, in); err != nil {
		return issueops.CreateBatchRequest{}, err
	}
	items := make([]issueops.BatchCreateItem, 0, len(templates))
	for _, template := range templates {
		dependencies, err := parseMarkdownDependencies(template.Dependencies, template.Title)
		if err != nil {
			return issueops.CreateBatchRequest{}, HandleError("%v", err)
		}
		items = append(items, issueops.BatchCreateItem{
			Issue: &types.Issue{
				Title:              template.Title,
				Description:        template.Description,
				Design:             template.Design,
				AcceptanceCriteria: template.AcceptanceCriteria,
				Status:             types.StatusOpen,
				Priority:           template.Priority,
				IssueType:          template.IssueType,
				Assignee:           template.Assignee,
				Labels:             template.Labels,
				Ephemeral:          in.ephemeral,
				NoHistory:          in.noHistory,
				MolType:            in.molType,
				CreatedBy:          in.createdBy,
				Owner:              in.owner,
			},
			Dependencies: dependencies,
		})
	}
	return issueops.CreateBatchRequest{
		Actor: markdownBatchActor(in),
		Items: items,
		// The entry both routes have always written, spelled once. The role's
		// own default would name a count and lose the file, which is the thing
		// `bd dolt log` is read for after a bulk create.
		Provenance: fmt.Sprintf("bd: create %d issue(s) from %s", len(templates), in.markdownFile),
	}, nil
}

// markdownBatchActor is the actor a `--file` create is attributed to. The role
// refuses an empty one, and "bd" is the fallback this command has always used
// when nothing named a person.
func markdownBatchActor(in createInput) string {
	if in.createdBy != "" {
		return in.createdBy
	}
	if actor != "" {
		return actor
	}
	return "bd"
}

// lintMarkdownTemplates applies the workspace's validation.on-create policy to
// every template, the same policy the single-issue create applies to its one
// issue.
func lintMarkdownTemplates(templates []*IssueTemplate, in createInput) error {
	if in.validationMode != "error" && in.validationMode != "warn" {
		return nil
	}
	for _, template := range templates {
		lintIssue := &types.Issue{
			IssueType:          template.IssueType,
			Description:        template.Description,
			AcceptanceCriteria: template.AcceptanceCriteria,
		}
		if err := validation.LintIssue(lintIssue); err != nil {
			if in.validationMode == "error" {
				return HandleError("template %q: %v", template.Title, err)
			}
			fmt.Fprintf(os.Stderr, "%s template %q: %v\n", ui.RenderWarn("⚠"), template.Title, err)
		}
	}
	return nil
}

// parseMarkdownDependencies reads a template's `### Dependencies` section as
// the role's edge specs. `type:target` names the type; a bare target blocks.
func parseMarkdownDependencies(deps []string, templateTitle string) ([]issueops.CreateDependency, error) {
	var out []issueops.CreateDependency
	for _, raw := range deps {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		var depType types.DependencyType
		var target string
		if strings.Contains(raw, ":") {
			parts := strings.SplitN(raw, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid dependency format %q for issue %q", raw, templateTitle)
			}
			depType = types.DependencyType(strings.TrimSpace(parts[0]))
			target = strings.TrimSpace(parts[1])
		} else {
			depType = types.DepBlocks
			target = raw
		}
		if !depType.IsValid() {
			return nil, fmt.Errorf("invalid dependency type %q for issue %q", depType, templateTitle)
		}
		out = append(out, issueops.CreateDependency{Type: depType, TargetID: target})
	}
	return out, nil
}

// reportMarkdownBatch prints what the batch created, in the one shape both
// routes print.
func reportMarkdownBatch(issues []*types.Issue, in createInput) error {
	if in.jsonOutput {
		return outputJSON(issues)
	}
	fmt.Printf("%s Created %d issues from %s:\n", ui.RenderPass("✓"), len(issues), in.markdownFile)
	for _, issue := range issues {
		fmt.Printf("  %s: %s [P%d, %s]\n", issue.ID, issue.Title, issue.Priority, issue.IssueType)
	}
	return nil
}
