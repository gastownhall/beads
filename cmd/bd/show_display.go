package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// displayShowIssue displays a single issue (reusable for watch mode)
// Matches the full bd show output: header, metadata, content, labels, deps, comments.
func displayShowIssue(ctx context.Context, issueID string) {
	// Use proper ID resolution (handles partial IDs and routed IDs)
	result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
	if result != nil {
		defer result.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching issue: %v\n", err)
		return
	}
	if result == nil || result.Issue == nil {
		fmt.Printf("Issue not found: %s\n", issueID)
		return
	}
	issue := result.Issue
	issueStore := result.Store

	// Display the issue header and metadata
	fmt.Println(formatIssueHeader(issue))
	fmt.Println(formatIssueMetadata(issue))

	// Content sections (matches standard bd show order)
	if issue.Description != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
	}
	if issue.Design != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), ui.RenderMarkdown(issue.Design))
	}
	if issue.Notes != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), ui.RenderMarkdown(issue.Notes))
	}
	if issue.AcceptanceCriteria != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), ui.RenderMarkdown(issue.AcceptanceCriteria))
	}

	// Labels
	labels, _ := issueStore.GetLabels(ctx, issue.ID)
	if len(labels) > 0 {
		fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(labels, ", "))
	}

	// Dependencies (what this issue depends on)
	relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)
	depsWithMeta, _ := issueStore.GetDependenciesWithMetadata(ctx, issue.ID)

	// Resolve external deps via routing (bd-k0pfm)
	if externalDeps, err := resolveExternalDepsViaRouting(ctx, issueStore, issue.ID); err == nil {
		depsWithMeta = append(depsWithMeta, externalDeps...)
	}

	if len(depsWithMeta) > 0 {
		var blocks, parent, discovered []*types.IssueWithDependencyMetadata
		for _, dep := range depsWithMeta {
			switch dep.DependencyType {
			case types.DepBlocks:
				blocks = append(blocks, dep)
			case types.DepParentChild:
				parent = append(parent, dep)
			case types.DepRelated, types.DepRelatesTo:
				relatedSeen[dep.ID] = dep
			case types.DepDiscoveredFrom:
				discovered = append(discovered, dep)
			default:
				blocks = append(blocks, dep)
			}
		}
		if len(parent) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("PARENT"))
			for _, dep := range parent {
				fmt.Println(formatDependencyLine("↑", dep))
			}
		}
		if len(blocks) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("DEPENDS ON"))
			for _, dep := range blocks {
				fmt.Println(formatDependencyLine("→", dep))
			}
		}
		if len(discovered) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("DISCOVERED FROM"))
			for _, dep := range discovered {
				fmt.Println(formatDependencyLine("◊", dep))
			}
		}
	}

	// Dependents (what depends on this issue)
	dependentsWithMeta, _ := issueStore.GetDependentsWithMetadata(ctx, issue.ID)
	if len(dependentsWithMeta) > 0 {
		var blocks, children, discovered []*types.IssueWithDependencyMetadata
		for _, dep := range dependentsWithMeta {
			switch dep.DependencyType {
			case types.DepBlocks:
				blocks = append(blocks, dep)
			case types.DepParentChild:
				children = append(children, dep)
			case types.DepRelated, types.DepRelatesTo:
				relatedSeen[dep.ID] = dep
			case types.DepDiscoveredFrom:
				discovered = append(discovered, dep)
			default:
				blocks = append(blocks, dep)
			}
		}
		if len(children) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("CHILDREN"))
			for _, dep := range children {
				fmt.Println(formatDependencyLine("↳", dep))
			}
		}
		if len(blocks) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("BLOCKS"))
			for _, dep := range blocks {
				fmt.Println(formatDependencyLine("←", dep))
			}
		}
		if len(discovered) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("DISCOVERED"))
			for _, dep := range discovered {
				fmt.Println(formatDependencyLine("◊", dep))
			}
		}
	}

	// Related (bidirectional, deduplicated)
	if len(relatedSeen) > 0 {
		fmt.Printf("\n%s\n", ui.RenderBold("RELATED"))
		for _, dep := range relatedSeen {
			fmt.Println(formatDependencyLine("↔", dep))
		}
	}

	// Comments
	comments, _ := issueStore.GetIssueComments(ctx, issue.ID)
	if len(comments) > 0 {
		fmt.Printf("\n%s\n", ui.RenderBold("COMMENTS"))
		for _, comment := range comments {
			fmt.Printf("  %s %s\n", ui.RenderMuted(comment.CreatedAt.UTC().Format("2006-01-02 15:04")), comment.Author)
			rendered := ui.RenderMarkdown(comment.Text)
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	fmt.Println()
}

// watchIssue watches for changes to an issue and auto-refreshes the display (GH#654)
func watchIssue(ctx context.Context, issueID string) {
	// Ensure we have a fresh database for watching (matches non-watch path)

	// Find .beads directory
	beadsDir := ".beads"
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: .beads directory not found\n")
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating watcher: %v\n", err)
		return
	}
	defer func() { _ = watcher.Close() }()

	// Watch the .beads directory
	if err := watcher.Add(beadsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error watching directory: %v\n", err)
		return
	}

	// Initial display
	displayShowIssue(ctx, issueID)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Debounce timer
	var debounceTimer *time.Timer
	debounceDelay := 500 * time.Millisecond

	for {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only react to writes on issues.jsonl or database files
			if event.Has(fsnotify.Write) {
				basename := filepath.Base(event.Name)
				if basename == "issues.jsonl" || strings.HasSuffix(basename, ".db") {
					// Debounce rapid changes
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceTimer = time.AfterFunc(debounceDelay, func() {
						displayShowIssue(ctx, issueID)
						fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
					})
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "Watcher error: %v\n", err)
		}
	}
}
