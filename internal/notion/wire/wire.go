package wire

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steveyegge/beads/internal/notion/state"
)

const (
	SchemaVersion            = "beads/v1"
	DefaultDatabaseTitle     = "Beads Issues"
	DefaultViewName          = "All Issues"
	ArchiveSupportModeUpdate = "update_page_command"
	ArchiveSupportModeNone   = "unsupported"
)

var (
	databaseTagRe     = regexp.MustCompile(`<database\b[^>]*>`)
	dataSourceTagRe   = regexp.MustCompile(`<data-source\b[^>]*>`)
	viewTagRe         = regexp.MustCompile(`<view\b[^>]*>`)
	attrReTemplate    = `%s="([^"]+)"`
	taggedContentTmpl = `<%s>\s*([\s\S]*?)\s*</%s>`
	viewURLAttrRe     = regexp.MustCompile(`url="(?:\{\{)?(view://[^"]+?)(?:\}\})?"`)
	viewURLDirectRe   = regexp.MustCompile(`(?:\{\{)?(view://[0-9a-z-]+)(?:\}\})?`)
	uuidRe            = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	compactUUIDRe     = regexp.MustCompile(`([0-9a-f]{32})`)
)

var RequiredProperties = []string{"Name", "Beads ID", "Status", "Priority", "Type", "Description"}
var OptionalProperties = []string{"Assignee", "Labels"}
var supportedPushIssueTypes = map[string]string{"bug": "Bug", "feature": "Feature", "task": "Task", "epic": "Epic", "chore": "Chore"}

type ViewInfo struct {
	ID   string  `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
	URL  string  `json:"url,omitempty"`
	Type *string `json:"type,omitempty"`
}

type DatabaseInfo struct {
	DatabaseID   string
	DatabaseURL  string
	DataSourceID string
	Views        []ViewInfo
}

type SchemaStatus struct {
	Checked         bool     `json:"checked,omitempty"`
	Required        []string `json:"required,omitempty"`
	Optional        []string `json:"optional,omitempty"`
	Detected        []string `json:"detected,omitempty"`
	Missing         []string `json:"missing,omitempty"`
	OptionalMissing []string `json:"optional_missing,omitempty"`
}

type ArchiveCapability struct {
	Supported         bool     `json:"supported"`
	Mode              string   `json:"mode,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	SupportedCommands []string `json:"supported_commands,omitempty"`
}

type AuthUser struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Type  string `json:"type,omitempty"`
}

type DoctorEntry struct {
	BeadsID       string `json:"beads_id"`
	PageID        string `json:"page_id"`
	Status        string `json:"status"`
	Message       string `json:"message,omitempty"`
	ActualBeadsID string `json:"actual_beads_id,omitempty"`
	NotionPageID  string `json:"notion_page_id,omitempty"`
	Title         string `json:"title,omitempty"`
}

type DoctorSummary struct {
	OK                    bool `json:"ok"`
	TotalCount            int  `json:"total_count,omitempty"`
	OKCount               int  `json:"ok_count,omitempty"`
	MissingPageCount      int  `json:"missing_page_count,omitempty"`
	IDDriftCount          int  `json:"id_drift_count,omitempty"`
	PropertyMismatchCount int  `json:"property_mismatch_count,omitempty"`
}

type Issue struct {
	ID           string   `json:"id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty"`
	Status       string   `json:"status,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Type         string   `json:"type,omitempty"`
	IssueType    string   `json:"issue_type,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	ExternalRef  string   `json:"external_ref,omitempty"`
	NotionPageID string   `json:"notion_page_id,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

type PushIssue struct {
	ID          string
	Title       string
	Description string
	Status      string
	Priority    string
	Type        string
	IssueType   string
	RawType     string
	Assignee    string
	Labels      []string
}

type PushIssueSet struct {
	Issues         []PushIssue `json:"issues"`
	ExistingIssues []Issue     `json:"existing_issues,omitempty"`
}

func SnapshotFromIssue(issue Issue) *state.ManagedIssueSnapshot {
	return &state.ManagedIssueSnapshot{
		ID:           strings.TrimSpace(issue.ID),
		Title:        strings.TrimSpace(issue.Title),
		Description:  strings.TrimSpace(issue.Description),
		Status:       strings.TrimSpace(issue.Status),
		Priority:     strings.TrimSpace(issue.Priority),
		IssueType:    firstNonEmpty(strings.TrimSpace(issue.IssueType), strings.TrimSpace(issue.Type)),
		Assignee:     strings.TrimSpace(issue.Assignee),
		Labels:       append([]string(nil), issue.Labels...),
		ExternalRef:  strings.TrimSpace(issue.ExternalRef),
		NotionPageID: strings.TrimSpace(issue.NotionPageID),
		CreatedAt:    strings.TrimSpace(issue.CreatedAt),
		UpdatedAt:    strings.TrimSpace(issue.UpdatedAt),
	}
}

func IssueFromSnapshot(snapshot *state.ManagedIssueSnapshot) Issue {
	if snapshot == nil {
		return Issue{}
	}
	issueType := strings.TrimSpace(snapshot.IssueType)
	return Issue{
		ID:           strings.TrimSpace(snapshot.ID),
		Title:        strings.TrimSpace(snapshot.Title),
		Description:  strings.TrimSpace(snapshot.Description),
		Status:       strings.TrimSpace(snapshot.Status),
		Priority:     strings.TrimSpace(snapshot.Priority),
		Type:         issueType,
		IssueType:    issueType,
		Assignee:     strings.TrimSpace(snapshot.Assignee),
		Labels:       append([]string(nil), snapshot.Labels...),
		ExternalRef:  strings.TrimSpace(snapshot.ExternalRef),
		NotionPageID: strings.TrimSpace(snapshot.NotionPageID),
		CreatedAt:    strings.TrimSpace(snapshot.CreatedAt),
		UpdatedAt:    strings.TrimSpace(snapshot.UpdatedAt),
	}
}

func (issue PushIssue) NormalizedType() string {
	return firstNonEmpty(strings.TrimSpace(issue.Type), strings.TrimSpace(issue.IssueType))
}

func (issue PushIssue) TypeValue() string {
	return firstNonEmpty(strings.TrimSpace(issue.RawType), issue.NormalizedType())
}

func (issue PushIssue) HasSupportedType() bool {
	rawType := strings.TrimSpace(issue.RawType)
	if rawType != "" {
		return SupportsPushIssueType(rawType)
	}
	return issue.NormalizedType() == "" || SupportsPushIssueType(issue.NormalizedType())
}

func SupportsPushIssueType(value string) bool {
	normalized := NormalizePushIssueType(value)
	if normalized == "" {
		return strings.TrimSpace(value) == ""
	}
	_, ok := supportedPushIssueTypes[normalized]
	return ok
}

func NormalizePushIssueType(value string) string {
	return normalizeEnumValue(value, supportedPushIssueTypes)
}

func ExtractText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		switch v := content.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		default:
			data, err := json.Marshal(v)
			if err == nil {
				parts = append(parts, string(data))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func ResultJSONMap(result *mcp.CallToolResult, operation string) (map[string]any, error) {
	if result == nil {
		return nil, fmt.Errorf("%s returned no result", operation)
	}
	if result.IsError {
		if err := result.GetError(); err != nil {
			return nil, fmt.Errorf("%s returned tool error: %w", operation, err)
		}
		text := strings.TrimSpace(ExtractText(result))
		if text == "" {
			return nil, fmt.Errorf("%s returned an unspecified tool error", operation)
		}
		return nil, fmt.Errorf("%s returned tool error: %s", operation, text)
	}
	if mapped, ok := result.StructuredContent.(map[string]any); ok {
		return mapped, nil
	}
	text := strings.TrimSpace(ExtractText(result))
	if text == "" {
		return nil, fmt.Errorf("%s returned an empty text payload", operation)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("%s did not return parseable JSON: %w", operation, err)
	}
	return parsed, nil
}

func ResultText(result *mcp.CallToolResult, operation string) (string, error) {
	if result == nil {
		return "", fmt.Errorf("%s returned no result", operation)
	}
	if result.IsError {
		if err := result.GetError(); err != nil {
			return "", fmt.Errorf("%s returned tool error: %w", operation, err)
		}
		text := strings.TrimSpace(ExtractText(result))
		if text == "" {
			return "", fmt.Errorf("%s returned an unspecified tool error", operation)
		}
		return "", fmt.Errorf("%s returned tool error: %s", operation, text)
	}
	if parsed, err := ResultJSONMap(result, operation); err == nil {
		for _, key := range []string{"result", "text"} {
			if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
	}
	fallback := strings.TrimSpace(ExtractText(result))
	if fallback == "" {
		return "", fmt.Errorf("%s did not include a text result payload", operation)
	}
	return fallback, nil
}

func ExtractBeadsDatabaseInfoFromText(text string) DatabaseInfo {
	databaseTag := databaseTagRe.FindString(text)
	dataSourceTag := dataSourceTagRe.FindString(text)
	viewMatches := viewTagRe.FindAllString(text, -1)
	views := make([]ViewInfo, 0, len(viewMatches))
	for _, raw := range viewMatches {
		url := extractAttr(raw, "url")
		if url == "" {
			continue
		}
		views = append(views, ViewInfo{
			Name: stringPtr(extractAttr(raw, "name")),
			URL:  url,
			Type: stringPtr(extractAttr(raw, "type")),
		})
	}
	databaseURL := extractAttr(databaseTag, "url")
	dataSourceURL := extractAttr(dataSourceTag, "url")
	return DatabaseInfo{
		DatabaseID:   ExtractPageIDFromURL(databaseURL),
		DatabaseURL:  databaseURL,
		DataSourceID: strings.TrimPrefix(dataSourceURL, "collection://"),
		Views:        views,
	}
}

func DetectBeadsPropertiesFromFetchText(text string) []string {
	state := extractTaggedJSONRecord(text, "data-source-state")
	if state == nil {
		return nil
	}
	schema, ok := state["schema"].(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func ExtractViewURLFromText(text string) string {
	if match := viewURLAttrRe.FindStringSubmatch(text); len(match) > 1 {
		return match[1]
	}
	if match := viewURLDirectRe.FindStringSubmatch(text); len(match) > 1 {
		return match[1]
	}
	return ""
}

func ExtractPageIDFromURL(url string) string {
	if match := uuidRe.FindStringSubmatch(url); len(match) > 1 {
		return match[1]
	}
	if match := compactUUIDRe.FindStringSubmatch(url); len(match) > 1 {
		compact := match[1]
		return strings.Join([]string{
			compact[0:8], compact[8:12], compact[12:16], compact[16:20], compact[20:32],
		}, "-")
	}
	return ""
}

func AssessSchema(detected []string, checked bool) SchemaStatus {
	detectedCopy := append([]string(nil), detected...)
	slices.Sort(detectedCopy)
	set := map[string]struct{}{}
	for _, item := range detectedCopy {
		set[item] = struct{}{}
	}
	missing := []string{}
	optionalMissing := []string{}
	for _, name := range RequiredProperties {
		if _, ok := set[name]; !ok {
			missing = append(missing, name)
		}
	}
	for _, name := range OptionalProperties {
		if _, ok := set[name]; !ok {
			optionalMissing = append(optionalMissing, name)
		}
	}
	return SchemaStatus{
		Checked:         checked,
		Required:        append([]string(nil), RequiredProperties...),
		Optional:        append([]string(nil), OptionalProperties...),
		Detected:        detectedCopy,
		Missing:         missing,
		OptionalMissing: optionalMissing,
	}
}

func FilterPropertiesBySchema(properties map[string]any, schema SchemaStatus) map[string]any {
	filtered := make(map[string]any, len(properties))
	missingOptional := make(map[string]struct{}, len(schema.OptionalMissing))
	for _, name := range schema.OptionalMissing {
		missingOptional[name] = struct{}{}
	}
	for key, value := range properties {
		if _, ok := missingOptional[key]; ok {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func DetectArchiveSupport(tools []*mcp.Tool) ArchiveCapability {
	var updatePageTool *mcp.Tool
	for _, tool := range tools {
		if tool != nil && tool.Name == "notion-update-page" {
			updatePageTool = tool
			break
		}
	}
	commands := extractToolCommandEnumOptions(updatePageTool)
	if slices.Contains(commands, "archive") {
		return ArchiveCapability{Supported: true, Mode: ArchiveSupportModeUpdate, SupportedCommands: commands}
	}
	reason := "The current live Notion MCP does not expose archive support on notion-update-page"
	if len(commands) > 0 {
		reason = "The current live Notion MCP only exposes notion-update-page commands: " + strings.Join(commands, ", ")
	}
	return ArchiveCapability{Supported: false, Mode: ArchiveSupportModeNone, Reason: reason, SupportedCommands: commands}
}

func NormalizePageFetchPayload(text string, payload map[string]any) (Issue, error) {
	properties := extractTaggedJSONRecord(text, "properties")
	if properties == nil {
		return Issue{}, fmt.Errorf("notion page fetch did not include a <properties> JSON block")
	}
	beadsID := pickString(properties, "Beads ID", "beads_id")
	title := pickString(properties, "Name", "title")
	if beadsID == "" || title == "" {
		return Issue{}, fmt.Errorf("missing Name or Beads ID in fetched page")
	}
	pageURL := pickString(properties, "url")
	issueType := NormalizePushIssueType(pickString(properties, "Type", "type"))
	return Issue{
		ID:           beadsID,
		Title:        title,
		Description:  pickString(properties, "Description", "description"),
		Status:       normalizeEnumValue(pickString(properties, "Status", "status"), map[string]string{"open": "Open", "in_progress": "In Progress", "blocked": "Blocked", "deferred": "Deferred", "closed": "Closed"}),
		Priority:     normalizeEnumValue(pickString(properties, "Priority", "priority"), map[string]string{"critical": "Critical", "high": "High", "medium": "Medium", "low": "Low", "backlog": "Backlog"}),
		Type:         issueType,
		IssueType:    issueType,
		Assignee:     pickString(properties, "Assignee", "assignee"),
		Labels:       normalizeLabels(valueToStringArray(properties["Labels"], properties["labels"])),
		ExternalRef:  pageURL,
		NotionPageID: ExtractPageIDFromURL(pageURL),
		CreatedAt:    pickString(payload, "created_at", "createdTime"),
		UpdatedAt:    pickString(payload, "updated_at", "updatedTime"),
	}, nil
}

func ParsePushInput(r io.Reader) (PushIssueSet, error) {
	var raw any
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return PushIssueSet{}, fmt.Errorf("beads input could not be parsed: %w", err)
	}
	items, existingIssues, err := normalizePushInput(raw)
	if err != nil {
		return PushIssueSet{}, err
	}
	return PushIssueSet{Issues: items, ExistingIssues: existingIssues}, nil
}

func normalizePushInput(raw any) ([]PushIssue, []Issue, error) {
	var existingIssues []Issue
	if mapped, ok := raw.(map[string]any); ok {
		if existingRaw, ok := mapped["existing_issues"]; ok {
			var err error
			existingIssues, err = normalizeExistingIssueArray(existingRaw)
			if err != nil {
				return nil, nil, err
			}
		}
		if issuesRaw, ok := mapped["issues"]; ok {
			raw = issuesRaw
		}
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("expected a JSON array or an object with an issues array")
	}
	issues := make([]PushIssue, 0, len(values))
	for _, item := range values {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("each issue must be a JSON object")
		}
		id := pickString(record, "id", "beads_id")
		title := pickString(record, "title", "Name")
		if id == "" || title == "" {
			return nil, nil, fmt.Errorf("each issue must include id and title")
		}
		rawIssueType := pickString(record, "type", "issue_type")
		issueType := NormalizePushIssueType(rawIssueType)
		issues = append(issues, PushIssue{
			ID:          id,
			Title:       title,
			Description: pickString(record, "description"),
			Status:      normalizeEnumValue(pickString(record, "status"), map[string]string{"open": "Open", "in_progress": "In Progress", "blocked": "Blocked", "deferred": "Deferred", "closed": "Closed"}),
			Priority:    normalizeEnumValue(pickString(record, "priority"), map[string]string{"critical": "Critical", "high": "High", "medium": "Medium", "low": "Low", "backlog": "Backlog"}),
			Type:        issueType,
			IssueType:   issueType,
			RawType:     rawIssueType,
			Assignee:    pickString(record, "assignee"),
			Labels:      normalizeLabels(valueToStringArray(record["labels"])),
		})
	}
	return issues, existingIssues, nil
}

func normalizeExistingIssueArray(raw any) ([]Issue, error) {
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("existing_issues must be a JSON array")
	}
	issues := make([]Issue, 0, len(values))
	for _, item := range values {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each existing issue must be a JSON object")
		}
		id := pickString(record, "id", "beads_id")
		if id == "" {
			return nil, fmt.Errorf("each existing issue must include id")
		}
		issueType := NormalizePushIssueType(pickString(record, "type", "issue_type"))
		externalRef := pickString(record, "external_ref", "url")
		issues = append(issues, Issue{
			ID:           id,
			Title:        pickString(record, "title", "Name"),
			Description:  pickString(record, "description"),
			Status:       normalizeEnumValue(pickString(record, "status"), map[string]string{"open": "Open", "in_progress": "In Progress", "blocked": "Blocked", "deferred": "Deferred", "closed": "Closed"}),
			Priority:     normalizeEnumValue(pickString(record, "priority"), map[string]string{"critical": "Critical", "high": "High", "medium": "Medium", "low": "Low", "backlog": "Backlog"}),
			Type:         issueType,
			IssueType:    issueType,
			Assignee:     pickString(record, "assignee"),
			Labels:       normalizeLabels(valueToStringArray(record["labels"])),
			ExternalRef:  externalRef,
			NotionPageID: firstNonEmpty(pickString(record, "notion_page_id", "page_id"), ExtractPageIDFromURL(externalRef)),
			CreatedAt:    pickString(record, "created_at"),
			UpdatedAt:    pickString(record, "updated_at"),
		})
	}
	return issues, nil
}

func FindDuplicateBeadsIDs(ids []string) []string {
	counts := map[string]int{}
	for _, id := range ids {
		counts[id]++
	}
	duplicates := []string{}
	for id, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, id)
		}
	}
	slices.Sort(duplicates)
	return duplicates
}

func BuildProperties(issue PushIssue) map[string]any {
	properties := map[string]any{
		"title":    issue.Title,
		"Beads ID": issue.ID,
	}
	if issue.Status != "" {
		properties["Status"] = toDisplay(issue.Status)
	}
	if issue.Priority != "" {
		properties["Priority"] = toDisplay(issue.Priority)
	}
	if firstType := firstNonEmpty(issue.Type, issue.IssueType); firstType != "" {
		properties["Type"] = toDisplay(firstType)
	}
	if issue.Description != "" {
		properties["Description"] = issue.Description
	}
	if issue.Assignee != "" {
		properties["Assignee"] = issue.Assignee
	}
	if len(issue.Labels) > 0 {
		properties["Labels"] = issue.Labels
	}
	return properties
}

func BuildUpdateProperties(issue PushIssue) map[string]any {
	properties := BuildProperties(issue)
	properties["Description"] = issue.Description
	properties["Assignee"] = issue.Assignee
	properties["Labels"] = issue.Labels
	return properties
}

func BuildDoctorEntry(expectedBeadsID, pageID, actualBeadsID, notionPageID, title string) DoctorEntry {
	if actualBeadsID != expectedBeadsID {
		return DoctorEntry{
			BeadsID:       expectedBeadsID,
			PageID:        pageID,
			Status:        "id_drift",
			Message:       fmt.Sprintf("Expected Beads ID %s, but Notion page reports %s", expectedBeadsID, actualBeadsID),
			ActualBeadsID: actualBeadsID,
			NotionPageID:  notionPageID,
			Title:         title,
		}
	}
	if notionPageID != "" && notionPageID != pageID {
		return DoctorEntry{
			BeadsID:       expectedBeadsID,
			PageID:        pageID,
			Status:        "property_mismatch",
			Message:       fmt.Sprintf("Fetched page %s normalized to page id %s", pageID, notionPageID),
			ActualBeadsID: actualBeadsID,
			NotionPageID:  notionPageID,
			Title:         title,
		}
	}
	return DoctorEntry{
		BeadsID:       expectedBeadsID,
		PageID:        pageID,
		Status:        "ok",
		ActualBeadsID: actualBeadsID,
		NotionPageID:  notionPageID,
		Title:         title,
	}
}

func SummarizeDoctorEntries(entries []DoctorEntry) DoctorSummary {
	total := len(entries)
	okCount := 0
	missingPageCount := 0
	idDriftCount := 0
	propertyMismatchCount := 0
	for _, entry := range entries {
		switch entry.Status {
		case "ok":
			okCount++
		case "missing_page":
			missingPageCount++
		case "id_drift":
			idDriftCount++
		case "property_mismatch":
			propertyMismatchCount++
		}
	}
	return DoctorSummary{
		OK:                    total == okCount,
		TotalCount:            total,
		OKCount:               okCount,
		MissingPageCount:      missingPageCount,
		IDDriftCount:          idDriftCount,
		PropertyMismatchCount: propertyMismatchCount,
	}
}

func ExtractSelfUser(payload map[string]any) *AuthUser {
	var candidate map[string]any
	if results, ok := payload["results"].([]any); ok && len(results) > 0 {
		candidate, _ = results[0].(map[string]any)
	}
	if candidate == nil {
		candidate = payload
	}
	if candidate == nil {
		return nil
	}
	return &AuthUser{
		ID:    pickString(candidate, "id"),
		Name:  pickString(candidate, "name"),
		Email: pickString(candidate, "email"),
		Type:  pickString(candidate, "type"),
	}
}

func ExtractPageIdentityFromText(text string) (string, string, string, error) {
	properties := extractTaggedJSONRecord(text, "properties")
	if properties == nil {
		return "", "", "", fmt.Errorf("notion page fetch did not include a <properties> JSON block")
	}
	beadsID := pickString(properties, "Beads ID", "beads_id")
	title := pickString(properties, "Name", "title")
	pageURL := pickString(properties, "url")
	notionPageID := ExtractPageIDFromURL(pageURL)
	if beadsID == "" || title == "" {
		return "", "", "", fmt.Errorf("missing Name or Beads ID in fetched page")
	}
	return beadsID, notionPageID, title, nil
}

func extractToolCommandEnumOptions(tool *mcp.Tool) []string {
	if tool == nil {
		return nil
	}
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		return nil
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	command, ok := properties["command"].(map[string]any)
	if !ok {
		return nil
	}
	rawEnum, ok := command["enum"].([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(rawEnum))
	for _, raw := range rawEnum {
		if value, ok := raw.(string); ok {
			values = append(values, value)
		}
	}
	return values
}

func pickString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractTaggedJSONRecord(text, tag string) map[string]any {
	re := regexp.MustCompile(fmt.Sprintf(taggedContentTmpl, regexp.QuoteMeta(tag), regexp.QuoteMeta(tag)))
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(match[1])), &parsed); err != nil {
		return nil
	}
	return parsed
}

func extractAttr(tag, name string) string {
	if tag == "" {
		return ""
	}
	re := regexp.MustCompile(fmt.Sprintf(attrReTemplate, regexp.QuoteMeta(name)))
	match := re.FindStringSubmatch(tag)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(match[1], "{{"), "}}")
}

func stringPtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	copy := v
	return &copy
}

func toDisplay(value string) string {
	switch value {
	case "in_progress":
		return "In Progress"
	default:
		if value == "" {
			return ""
		}
		return strings.ToUpper(value[:1]) + strings.ReplaceAll(value[1:], "_", " ")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeEnumValue(value string, labels map[string]string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if normalized == "" {
		return ""
	}
	if _, ok := labels[normalized]; ok {
		return normalized
	}
	for key, label := range labels {
		if strings.EqualFold(label, value) {
			return key
		}
	}
	return normalized
}

func valueToStringArray(values ...any) []string {
	for _, raw := range values {
		items, ok := raw.([]any)
		if !ok {
			continue
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, strings.TrimSpace(value))
			}
		}
		return out
	}
	return nil
}

func normalizeLabels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := map[string]struct{}{}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
