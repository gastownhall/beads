// Package github provides client and data types for the GitHub REST API.
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MappingConfig configures how GitHub fields map to beads fields.
type MappingConfig struct {
	PriorityMap  map[string]int    // priority label value -> beads priority (0-4)
	StateMap     map[string]string // GitHub state -> beads status
	LabelTypeMap map[string]string // type label value -> beads issue type

	// IssuePrefix is the local bead ID prefix (e.g. "bd"). When set, a bead
	// ID extracted from a synced issue body must start with "<prefix>-" or it
	// is discarded, so a foreign or fabricated marker cannot mint or bind a
	// local bead. Empty disables the check (Tracker.Init always sets it).
	IssuePrefix string
}

// DefaultMappingConfig returns the default mapping configuration.
// Uses exported mapping constants from types.go as the single source of truth.
func DefaultMappingConfig() *MappingConfig {
	// Copy PriorityMapping to avoid external modification
	priorityMap := make(map[string]int, len(PriorityMapping))
	for k, v := range PriorityMapping {
		priorityMap[k] = v
	}

	// Copy typeMapping to avoid external modification
	labelTypeMap := make(map[string]string, len(typeMapping))
	for k, v := range typeMapping {
		labelTypeMap[k] = v
	}

	return &MappingConfig{
		PriorityMap: priorityMap,
		// StateMap maps GitHub states to beads statuses
		StateMap: map[string]string{
			"open":   StatusMapping["open"],
			"closed": StatusMapping["closed"],
		},
		LabelTypeMap: labelTypeMap,
	}
}

// priorityFromLabels extracts priority from GitHub labels.
// Returns default priority (2 = medium) if no priority label found.
func priorityFromLabels(labels []string, config *MappingConfig) int {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "priority" {
			if p, ok := config.PriorityMap[strings.ToLower(value)]; ok {
				return p
			}
		}
	}
	return 2 // Default to medium
}

// statusFromLabelsAndState determines beads status from GitHub labels and state.
// GitHub's closed state takes precedence over status labels.
func statusFromLabelsAndState(labels []string, state string, config *MappingConfig) string {
	// Closed state always wins
	if state == "closed" {
		return "closed"
	}

	// Check for status label
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "status" {
			normalized := strings.ToLower(value)
			if normalized == "in_progress" {
				return "in_progress"
			}
			if normalized == "blocked" {
				return "blocked"
			}
			if normalized == "deferred" {
				return "deferred"
			}
		}
	}

	// Default: map GitHub state to beads status
	if s, ok := config.StateMap[state]; ok {
		return s
	}
	return "open"
}

// typeFromLabels extracts issue type from GitHub labels.
// Checks both scoped (type::bug) and bare (bug) labels.
// Returns "task" if no type label found.
func typeFromLabels(labels []string, config *MappingConfig) string {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "type" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
		// Also check bare labels (no prefix)
		if prefix == "" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
	}
	return "task" // Default to task
}

// GitHubIssueToBeads converts a GitHub Issue to a beads Issue.
func GitHubIssueToBeads(gh *Issue, config *MappingConfig) *IssueConversion {
	htmlURL := gh.HTMLURL
	sourceSystem := fmt.Sprintf("github:%s:%d", gh.HTMLURL, gh.Number)
	// Use a cleaner source system format if HTMLURL is not available
	if htmlURL == "" {
		sourceSystem = fmt.Sprintf("github:%d:%d", gh.ID, gh.Number)
	}

	labelNames := gh.LabelNames()

	// A bead ID embedded in the synced body is a consistency HINT, never an
	// identity source: the footer travels in the public, author-editable issue
	// body, so anyone can fabricate it. The sync engine only honors the hint
	// when the named local bead already carries this GitHub issue's URL as its
	// external_ref (see the pull path in internal/tracker/engine.go); in every
	// other case it imports the issue as new with a generated ID. Here we only
	// pre-filter IDs that cannot possibly be local (foreign or missing prefix).
	extractedID := ExtractBDIDFromGitHubSyncBlock(gh.Body)
	if config.IssuePrefix != "" && !strings.HasPrefix(extractedID, config.IssuePrefix+"-") {
		extractedID = ""
	}

	issue := &types.Issue{
		ID:           extractedID,
		Title:        gh.Title,
		Description:  StripGitHubSyncBlock(gh.Body),
		ExternalRef:  &htmlURL,
		SourceSystem: sourceSystem,
		IssueType:    types.IssueType(typeFromLabels(labelNames, config)),
		Priority:     priorityFromLabels(labelNames, config),
		Status:       types.Status(statusFromLabelsAndState(labelNames, gh.State, config)),
		Labels:       filterNonScopedLabels(labelNames),
	}

	// Set assignee from GitHub user
	if gh.Assignee != nil {
		issue.Assignee = gh.Assignee.Login
	}

	// Set timestamps
	if gh.CreatedAt != nil {
		issue.CreatedAt = *gh.CreatedAt
	}
	if gh.UpdatedAt != nil {
		issue.UpdatedAt = *gh.UpdatedAt
	}

	return &IssueConversion{
		Issue:        issue,
		Dependencies: []DependencyInfo{},
	}
}

// BeadsIssueToGitHubFields converts a beads Issue to GitHub API update fields.
func BeadsIssueToGitHubFields(issue *types.Issue, config *MappingConfig) map[string]interface{} {
	// Append the metadata block unless the description already ends with a
	// well-formed one (the engine's FormatDescription hook has already
	// rendered it). Marker text merely quoted in user prose does not count.
	body := issue.Description
	if StripGitHubSyncBlock(body) == body {
		body = RenderGitHubIssueBody(issue, nil, nil, "")
	}
	fields := map[string]interface{}{
		"title": issue.Title,
		"body":  body,
	}

	// Build labels from type, priority, and status
	var labels []string

	// Add type label
	if issue.IssueType != "" {
		labels = append(labels, "type::"+string(issue.IssueType))
	}

	// Add priority label
	priorityLabel := priorityToLabel(issue.Priority)
	if priorityLabel != "" {
		labels = append(labels, "priority::"+priorityLabel)
	}

	// Add status label (if not open or closed - those are handled by state)
	if issue.Status == types.StatusInProgress {
		labels = append(labels, "status::in_progress")
	} else if issue.Status == types.StatusBlocked {
		labels = append(labels, "status::blocked")
	} else if issue.Status == types.StatusDeferred {
		labels = append(labels, "status::deferred")
	}

	// Add any existing non-scoped labels
	labels = append(labels, issue.Labels...)

	fields["labels"] = labels

	// Set state for closed issues
	if issue.Status == types.StatusClosed {
		fields["state"] = "closed"
	} else {
		fields["state"] = "open"
	}

	return fields
}

// PushFieldsEqual reports whether a GitHub push would be a no-op by comparing
// only the fields a push can actually mutate: title, body, state, and the
// label set that BeadsIssueToGitHubFields would send. When these already match
// the remote issue, the push is redundant and can be skipped — this is what
// prevents `bd github sync --push-only` from re-PATCHing every issue on every
// run (gastownhall/beads#4214). Mirrors linear.PushFieldsEqual.
func PushFieldsEqual(local *types.Issue, remote *Issue, config *MappingConfig) bool {
	if local == nil || remote == nil {
		return false
	}
	if local.Title != remote.Title {
		return false
	}
	if local.Description != remote.Body {
		return false
	}

	desiredState := "open"
	if local.Status == types.StatusClosed {
		desiredState = "closed"
	}
	if !strings.EqualFold(desiredState, remote.State) {
		return false
	}

	// Compare the label set we would push against the remote's current labels.
	// Both include the scoped type::/priority::/status:: labels plus any
	// non-scoped labels, so an unchanged issue produces an identical set.
	desiredLabels, _ := BeadsIssueToGitHubFields(local, config)["labels"].([]string)
	return labelSetsEqual(desiredLabels, remote.LabelNames())
}

// PushContentHash returns a stable hex fingerprint of the fields a push would
// send to GitHub: title, body, desired state, and the order-independent label
// set produced by BeadsIssueToGitHubFields. The engine persists this hash in
// local_metadata after each push and compares it before fetching the remote
// issue, so an unchanged issue is skipped without any API call
// (gastownhall/beads#4214). It is derived from the same fields PushFieldsEqual
// compares, so the two agree on what "unchanged" means. Fields are
// length-prefixed before hashing so distinct field boundaries cannot collide.
func PushContentHash(local *types.Issue, config *MappingConfig) string {
	if local == nil {
		return ""
	}

	desiredState := "open"
	if local.Status == types.StatusClosed {
		desiredState = "closed"
	}

	desiredLabels, _ := BeadsIssueToGitHubFields(local, config)["labels"].([]string)
	sortedLabels := append([]string(nil), desiredLabels...)
	slices.Sort(sortedLabels)

	h := sha256.New()
	for _, s := range []string{local.Title, local.Description, desiredState, strings.Join(sortedLabels, "\x00")} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// labelSetsEqual reports whether a and b contain the same labels, ignoring
// order (GitHub does not preserve label order across a round-trip).
func labelSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// githubSyncStartMarker / githubSyncEndMarker delimit the generated Beads
// metadata block appended to synced GitHub issue bodies. They are only
// recognized when they occupy an entire line with no leading whitespace
// (line-anchored), so marker text quoted inside prose, list items, or
// indented code never counts as framing.
const (
	githubSyncStartMarker = "<!-- bd-github-sync: start -->"
	githubSyncEndMarker   = "<!-- bd-github-sync: end -->"
	githubSyncVersion     = "sync=github-v1"
)

// isGitHubSyncMarkerLine reports whether line consists solely of marker:
// no leading whitespace, and nothing after it except spaces, tabs, or a
// carriage return (so CRLF bodies still match).
func isGitHubSyncMarkerLine(line, marker string) bool {
	if !strings.HasPrefix(line, marker) {
		return false
	}
	return strings.TrimRight(line, " \t\r") == marker
}

// githubSyncMarkersBalanced reports whether every line-anchored sync marker
// in s participates in a properly paired, non-nested start/end block.
func githubSyncMarkersBalanced(s string) bool {
	open := false
	for _, line := range strings.Split(s, "\n") {
		switch {
		case isGitHubSyncMarkerLine(line, githubSyncStartMarker):
			if open {
				return false // nested start
			}
			open = true
		case isGitHubSyncMarkerLine(line, githubSyncEndMarker):
			if !open {
				return false // dangling end
			}
			open = false
		}
	}
	return !open // dangling start
}

// githubBodyLimit is GitHub's documented maximum issue/pull-request body
// length in bytes. RenderGitHubIssueBody keeps the rendered body under it
// (minus githubBodySafetyMargin) so a push is never rejected outright by the
// API for a body GitHub itself would refuse.
const githubBodyLimit = 65536

// githubBodySafetyMargin is subtracted from githubBodyLimit before comparing,
// leaving headroom for any GitHub-side normalization so a body sitting right
// at the boundary still round-trips cleanly.
const githubBodySafetyMargin = 256

// RenderGitHubIssueBody appends the generated Beads metadata block to the
// issue description. The description itself is preserved byte-for-byte, so
// StripGitHubSyncBlock is the exact inverse: Strip(Render(D)) == D.
//
// targetRepoURL is the HTML URL of the repository being pushed to (e.g.
// "https://github.com/owner/repo"); dependency links whose external refs
// verifiably live in that repository render as #<number>, everything else
// keeps its full URL so GitHub never resolves the number against the wrong
// repository. Empty means "unknown target": always emit full URLs.
//
// If the description already contains malformed (unpaired or nested)
// line-anchored sync markers, appending a block over them would let a later
// strip span stray markers and delete user text, so this refuses and returns
// the description unchanged. Sync still works via external_ref in that
// degraded mode; only the metadata footer is omitted.
//
// If the fully-rendered body would exceed githubBodyLimit, the dependency and
// dependent tasklist sections are dropped — they grow with the size of the
// dependency graph rather than with user content, so they are the safe thing
// to shed first. The description and the canonical footer (including the ID
// line Strip and Extract require) are always kept. If the body is still over
// the limit after dropping the tasklists, the description itself must be
// huge; this is left as-is rather than truncating user content, a pre-
// existing condition this function does not attempt to fix.
func RenderGitHubIssueBody(issue *types.Issue, dependencies, dependents []*types.IssueWithDependencyMetadata, targetRepoURL string) string {
	if issue == nil {
		return ""
	}

	base := issue.Description
	if !githubSyncMarkersBalanced(base) {
		return base
	}

	full := buildGitHubIssueBody(issue, base, dependencies, dependents, targetRepoURL)
	if len(full) <= githubBodyLimit-githubBodySafetyMargin {
		return full
	}
	return buildGitHubIssueBody(issue, base, nil, nil, targetRepoURL)
}

// buildGitHubIssueBody assembles the description, generated footer, and
// (when non-empty) the dependency/dependent tasklist sections. It performs no
// size checking; RenderGitHubIssueBody calls it once with the requested
// dependency lists and, if that exceeds the body limit, again with them
// dropped.
func buildGitHubIssueBody(issue *types.Issue, base string, dependencies, dependents []*types.IssueWithDependencyMetadata, targetRepoURL string) string {
	var b strings.Builder
	b.WriteString(base)
	if base != "" {
		b.WriteString("\n\n")
	}
	b.WriteString(githubSyncStartMarker + "\n")
	if strings.TrimSpace(issue.ID) != "" {
		b.WriteString(fmt.Sprintf("<!-- bd: %s %s -->\n", strings.TrimSpace(issue.ID), githubSyncVersion))
	}
	b.WriteString("## Beads\n\n")
	if strings.TrimSpace(issue.ID) != "" {
		b.WriteString(fmt.Sprintf("Source: `%s`\n", strings.TrimSpace(issue.ID)))
	}
	appendGitHubTasklistSection(&b, "Dependencies", dependencies, true, targetRepoURL)
	// "Dependents" is deliberately neutral: incoming edges include
	// non-blocking relationships (related, discovered-from, parent-child),
	// so a "Blocked by this issue" heading would mislabel them. The per-row
	// relationship labels carry the precise semantics.
	appendGitHubTasklistSection(&b, "Dependents", dependents, false, targetRepoURL)
	b.WriteString(githubSyncEndMarker)
	return b.String()
}

func appendGitHubTasklistSection(b *strings.Builder, title string, issues []*types.IssueWithDependencyMetadata, incoming bool, targetRepoURL string) {
	if len(issues) == 0 {
		return
	}
	b.WriteString("\n")
	b.WriteString("### ")
	b.WriteString(title)
	b.WriteString("\n")
	for _, item := range issues {
		if item == nil {
			continue
		}
		label := githubDependencyLabel(item.DependencyType, incoming)
		if item.Status == types.StatusClosed {
			b.WriteString("- [x] ")
		} else {
			b.WriteString("- [ ] ")
		}
		b.WriteString(githubTaskTarget(&item.Issue, targetRepoURL))
		if text := githubTaskTitle(item.Title); text != "" {
			b.WriteString(" — ")
			b.WriteString(text)
		}
		if item.ID != "" || label != "" {
			b.WriteString(" (")
			parts := make([]string, 0, 2)
			if item.ID != "" {
				parts = append(parts, fmt.Sprintf("`%s`", item.ID))
			}
			if label != "" {
				parts = append(parts, label)
			}
			b.WriteString(strings.Join(parts, ", "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
}

func githubDependencyLabel(depType types.DependencyType, incoming bool) string {
	switch depType {
	case types.DepBlocks:
		if incoming {
			return "blocked by"
		}
		return "blocks"
	case types.DepParentChild:
		if incoming {
			return "parent"
		}
		return "child"
	case types.DepWaitsFor:
		if incoming {
			return "waits for"
		}
		return "waited on by"
	case types.DepConditionalBlocks:
		if incoming {
			return "conditionally blocked by"
		}
		return "conditionally blocks"
	}
	return string(depType)
}

// githubTaskTarget renders the link target for one tasklist row. Only a ref
// that verifiably lives in targetRepoURL is shortened to #<number>; any other
// GitHub issue URL is emitted in full, because a bare #<number> is resolved by
// GitHub relative to the repository the body lives in and would point at an
// unrelated issue there.
func githubTaskTarget(issue *types.Issue, targetRepoURL string) string {
	if issue == nil {
		return "`unknown`"
	}
	if issue.ExternalRef != nil {
		ref := strings.TrimSpace(*issue.ExternalRef)
		if n := githubIssueNumberFromRef(ref); n != "" {
			if githubRefInRepo(ref, n, targetRepoURL) {
				return "#" + n
			}
			return ref
		}
	}
	if strings.TrimSpace(issue.ID) != "" {
		return fmt.Sprintf("`%s`", strings.TrimSpace(issue.ID))
	}
	return "`unknown`"
}

// githubRefInRepo reports whether ref is exactly the issue URL
// "<targetRepoURL>/issues/<num>" (case-insensitively, since GitHub hosts and
// owner/repo slugs are case-insensitive). Anything else — different repo,
// trailing fragments, unknown target — is not verifiably in-repo and must keep
// its full URL.
func githubRefInRepo(ref, num, targetRepoURL string) bool {
	target := strings.TrimRight(strings.TrimSpace(targetRepoURL), "/")
	if target == "" || num == "" {
		return false
	}
	return strings.EqualFold(ref, target+"/issues/"+num)
}

func githubIssueNumberFromRef(ref string) string {
	marker := "/issues/"
	idx := strings.LastIndex(ref, marker)
	if idx == -1 {
		return ""
	}
	rest := ref[idx+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	return rest[:end]
}

func githubTaskTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	// Escape HTML-comment delimiters so a hostile dependency title can never
	// smuggle a sync marker (or any comment) into the generated block. The
	// entities render as visible <!-- / --> text on GitHub. Titles are also
	// interpolated inline (never line-anchored), so this is defense in depth
	// on top of the line-anchored marker framing.
	title = strings.ReplaceAll(title, "<!--", "&lt;!--")
	title = strings.ReplaceAll(title, "-->", "--&gt;")
	return title
}

// githubSyncLine is one physical line of a body plus the byte offset of its
// first character in the original string.
type githubSyncLine struct {
	text   string
	offset int
}

func splitGitHubSyncLines(body string) []githubSyncLine {
	lines := make([]githubSyncLine, 0, strings.Count(body, "\n")+1)
	offset := 0
	for {
		nl := strings.IndexByte(body[offset:], '\n')
		if nl == -1 {
			lines = append(lines, githubSyncLine{text: body[offset:], offset: offset})
			return lines
		}
		lines = append(lines, githubSyncLine{text: body[offset : offset+nl], offset: offset})
		offset += nl + 1
	}
}

// canonicalBDIDLine scans lines[startLine+1:endLine] (the interior of a
// candidate block) for the line-anchored ID line RenderGitHubIssueBody emits:
// "<!-- bd: <id> sync=github-v1 -->". It returns the extracted ID and true
// for the first such line, or "", false if none is present. This is the same
// match ExtractBDIDFromGitHubSyncBlock uses to bind a GitHub issue to a bead,
// factored out so findTrailingGitHubSyncBlock can require it too: a trailing
// paired marker pair with no canonical ID line inside was never produced by
// Render (Render always emits it for a non-empty issue.ID), so it must be
// user text — e.g. a pasted example — that merely looks like the generated
// block, and stripping it would silently truncate the imported description.
func canonicalBDIDLine(lines []githubSyncLine, startLine, endLine int) (string, bool) {
	for j := startLine + 1; j < endLine; j++ {
		line := strings.TrimRight(lines[j].text, " \t\r")
		if !strings.HasPrefix(line, "<!-- bd:") || !strings.HasSuffix(line, "-->") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line[len("<!-- bd:") : len(line)-len("-->")]))
		id := ""
		hasToken := false
		for _, field := range fields {
			if field == githubSyncVersion {
				hasToken = true
				continue
			}
			if id != "" {
				id = "" // more than one candidate ID: malformed, reject
				break
			}
			id = field
		}
		if hasToken && id != "" {
			return id, true
		}
	}
	return "", false
}

// findTrailingGitHubSyncBlock locates the single well-formed generated block
// that RenderGitHubIssueBody appends: a line-anchored start marker, followed
// by content containing no other marker lines, closed by a line-anchored end
// marker that is the last non-blank line of the body, with everything before
// the block having balanced markers (Render never appends over unbalanced
// markers, so an unbalanced prefix means this block is user text, not ours),
// and containing the canonical ID line Render always emits (so a pasted,
// well-formed-looking example that merely ends a description is never
// mistaken for the generated footer). It returns the line index range
// [startLine, endLine] and true, or false if no such block exists.
func findTrailingGitHubSyncBlock(lines []githubSyncLine) (int, int, bool) {
	endLine := len(lines) - 1
	for endLine >= 0 && strings.TrimSpace(lines[endLine].text) == "" {
		endLine--
	}
	if endLine < 0 || !isGitHubSyncMarkerLine(lines[endLine].text, githubSyncEndMarker) {
		return 0, 0, false
	}
	startLine := -1
	for j := endLine - 1; j >= 0; j-- {
		if isGitHubSyncMarkerLine(lines[j].text, githubSyncEndMarker) {
			return 0, 0, false // another end marker inside the candidate block
		}
		if isGitHubSyncMarkerLine(lines[j].text, githubSyncStartMarker) {
			startLine = j
			break
		}
	}
	if startLine == -1 {
		return 0, 0, false
	}
	// Everything before the block must be balanced; otherwise a stray user
	// marker would be spanned and user text deleted.
	var prefix strings.Builder
	for j := 0; j < startLine; j++ {
		prefix.WriteString(lines[j].text)
		prefix.WriteString("\n")
	}
	if !githubSyncMarkersBalanced(prefix.String()) {
		return 0, 0, false
	}
	if _, ok := canonicalBDIDLine(lines, startLine, endLine); !ok {
		return 0, 0, false
	}
	return startLine, endLine, true
}

// StripGitHubSyncBlock removes the generated Beads sync footer from a GitHub
// issue body before storing it as the local issue description. It is the
// exact inverse of RenderGitHubIssueBody: only a single well-formed trailing
// block with line-anchored markers is removed, along with the separator
// Render inserted, so user text — including quoted or malformed marker text —
// is preserved byte-for-byte. Bodies without such a block are returned
// unchanged.
func StripGitHubSyncBlock(body string) string {
	lines := splitGitHubSyncLines(body)
	startLine, _, ok := findTrailingGitHubSyncBlock(lines)
	if !ok {
		return body
	}
	prefix := body[:lines[startLine].offset]
	// Render separates a non-empty description from the block with exactly
	// two newlines; remove up to two trailing newline sequences to undo it.
	for i := 0; i < 2; i++ {
		switch {
		case strings.HasSuffix(prefix, "\r\n"):
			prefix = prefix[:len(prefix)-2]
		case strings.HasSuffix(prefix, "\n"):
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

// ExtractBDIDFromGitHubSyncBlock returns the bead ID recorded in the
// generated sync footer, or "" when the body carries no well-formed trailing
// block. The ID line must be line-anchored inside the paired block and carry
// the sync=github-v1 token, so bare hand-placed `<!-- bd: ... -->` markers,
// marker-shaped prose, and quoted blocks outside the trailing block are all
// inert (they never bind a GitHub issue to a bead).
func ExtractBDIDFromGitHubSyncBlock(body string) string {
	lines := splitGitHubSyncLines(body)
	startLine, endLine, ok := findTrailingGitHubSyncBlock(lines)
	if !ok {
		return ""
	}
	id, _ := canonicalBDIDLine(lines, startLine, endLine)
	return id
}

// priorityToLabel converts beads priority (0-4) to GitHub priority label value.
func priorityToLabel(priority int) string {
	switch priority {
	case 0:
		return "critical"
	case 1:
		return "high"
	case 2:
		return "medium"
	case 3:
		return "low"
	case 4:
		return "none"
	default:
		return "medium"
	}
}

// filterNonScopedLabels returns only labels without scoped prefixes.
// Removes priority::*, status::*, and type::* labels.
func filterNonScopedLabels(labels []string) []string {
	var filtered []string
	for _, label := range labels {
		prefix, _ := parseLabelPrefix(label)
		// Skip scoped labels that we handle specially
		if prefix == "priority" || prefix == "status" || prefix == "type" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}
