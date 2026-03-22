package notion

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	PropertyTitle       = "Name"
	PropertyBeadsID     = "Beads ID"
	PropertyStatus      = "Status"
	PropertyPriority    = "Priority"
	PropertyType        = "Type"
	PropertyDescription = "Description"
	PropertyAssignee    = "Assignee"
	PropertyLabels      = "Labels"
)

var requiredSchemaProperties = map[string]string{
	PropertyTitle:       "title",
	PropertyBeadsID:     "rich_text",
	PropertyStatus:      "select",
	PropertyPriority:    "select",
	PropertyType:        "select",
	PropertyDescription: "rich_text",
	PropertyAssignee:    "rich_text",
	PropertyLabels:      "multi_select",
}

// StatusResponse is the machine-readable output from `bd notion status --json`.
type StatusResponse struct {
	Ready         bool               `json:"ready"`
	DataSourceID  string             `json:"data_source_id,omitempty"`
	ViewURL       string             `json:"view_url,omitempty"`
	SchemaVersion string             `json:"schema_version,omitempty"`
	Configured    bool               `json:"configured,omitempty"`
	SavedConfig   bool               `json:"saved_config_present,omitempty"`
	ConfigSource  string             `json:"config_source,omitempty"`
	Auth          *StatusAuth        `json:"auth,omitempty"`
	Database      *StatusDatabase    `json:"database,omitempty"`
	Views         []StatusView       `json:"views,omitempty"`
	Schema        *StatusSchema      `json:"schema,omitempty"`
	Archive       *ArchiveCapability `json:"archive,omitempty"`
	State         *StatusState       `json:"state,omitempty"`
	Error         string             `json:"error,omitempty"`
}

// StatusAuth describes authentication state.
type StatusAuth struct {
	OK   bool        `json:"ok"`
	User *StatusUser `json:"user,omitempty"`
}

// StatusUser describes the authenticated Notion user.
type StatusUser struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Type  string `json:"type,omitempty"`
}

// StatusDatabase describes the selected Notion database.
type StatusDatabase struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// StatusView describes a database view.
type StatusView struct {
	ID   string  `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
	URL  string  `json:"url,omitempty"`
	Type *string `json:"type,omitempty"`
}

// StatusSchema describes schema validation state.
type StatusSchema struct {
	Checked         bool     `json:"checked,omitempty"`
	Required        []string `json:"required,omitempty"`
	Optional        []string `json:"optional,omitempty"`
	Detected        []string `json:"detected,omitempty"`
	Missing         []string `json:"missing,omitempty"`
	OptionalMissing []string `json:"optional_missing,omitempty"`
}

// ArchiveCapability describes archive support visibility.
type ArchiveCapability struct {
	Supported         bool     `json:"supported"`
	Mode              string   `json:"mode,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	SupportedCommands []string `json:"supported_commands,omitempty"`
}

// StatusState captures local bridge state information.
type StatusState struct {
	Path           string         `json:"path,omitempty"`
	Present        bool           `json:"present,omitempty"`
	ManagedCount   int            `json:"managed_count,omitempty"`
	ViewConfigured bool           `json:"view_configured,omitempty"`
	DoctorSummary  *DoctorSummary `json:"doctor_summary,omitempty"`
}

// DoctorSummary captures machine-readable state health.
type DoctorSummary struct {
	OK                    bool `json:"ok"`
	TotalCount            int  `json:"total_count,omitempty"`
	OKCount               int  `json:"ok_count,omitempty"`
	MissingPageCount      int  `json:"missing_page_count,omitempty"`
	IDDriftCount          int  `json:"id_drift_count,omitempty"`
	PropertyMismatchCount int  `json:"property_mismatch_count,omitempty"`
}

// PulledIssue is the normalized issue record returned by integrated Notion pull.
type PulledIssue struct {
	ID           string         `json:"id,omitempty"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description,omitempty"`
	Status       string         `json:"status,omitempty"`
	Priority     string         `json:"priority,omitempty"`
	Type         string         `json:"type,omitempty"`
	IssueType    string         `json:"issue_type,omitempty"`
	Assignee     string         `json:"assignee,omitempty"`
	Labels       []string       `json:"labels,omitempty"`
	ExternalRef  string         `json:"external_ref,omitempty"`
	NotionPageID string         `json:"notion_page_id,omitempty"`
	CreatedAt    NullableString `json:"created_at,omitempty"`
	UpdatedAt    NullableString `json:"updated_at,omitempty"`
}

// NullableString accepts either a JSON string or null and normalizes null to the zero value.
type NullableString string

// UnmarshalJSON decodes a string or null into the nullable string.
func (s *NullableString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}

	*s = NullableString(value)
	return nil
}

type PushIssue struct {
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	IssueType   string   `json:"issue_type,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	ExternalRef string   `json:"external_ref,omitempty"`
}

type Person struct {
	Email string `json:"email,omitempty"`
}

type User struct {
	Object string  `json:"object,omitempty"`
	ID     string  `json:"id,omitempty"`
	Name   string  `json:"name,omitempty"`
	Type   string  `json:"type,omitempty"`
	Person *Person `json:"person,omitempty"`
}

type DataSource struct {
	Object     string                        `json:"object,omitempty"`
	ID         string                        `json:"id,omitempty"`
	URL        string                        `json:"url,omitempty"`
	Title      []RichText                    `json:"title,omitempty"`
	Properties map[string]DataSourceProperty `json:"properties,omitempty"`
}

type DataSourceProperty struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

type QueryDataSourceResponse struct {
	Results    []Page `json:"results,omitempty"`
	HasMore    bool   `json:"has_more,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type Page struct {
	Object         string                  `json:"object,omitempty"`
	ID             string                  `json:"id,omitempty"`
	URL            string                  `json:"url,omitempty"`
	InTrash        bool                    `json:"in_trash,omitempty"`
	Archived       bool                    `json:"archived,omitempty"`
	CreatedTime    time.Time               `json:"created_time,omitempty"`
	LastEditedTime time.Time               `json:"last_edited_time,omitempty"`
	Properties     map[string]PageProperty `json:"properties,omitempty"`
}

type PageProperty struct {
	ID          string         `json:"id,omitempty"`
	Type        string         `json:"type,omitempty"`
	Title       []RichText     `json:"title,omitempty"`
	RichText    []RichText     `json:"rich_text,omitempty"`
	Select      *SelectOption  `json:"select,omitempty"`
	MultiSelect []SelectOption `json:"multi_select,omitempty"`
}

type RichText struct {
	Type      string      `json:"type,omitempty"`
	PlainText string      `json:"plain_text,omitempty"`
	Text      *TextObject `json:"text,omitempty"`
}

type TextObject struct {
	Content string `json:"content,omitempty"`
}

type SelectOption struct {
	Name string `json:"name,omitempty"`
}

func DataSourceTitle(items []RichText) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		switch {
		case item.PlainText != "":
			parts = append(parts, item.PlainText)
		case item.Text != nil && item.Text.Content != "":
			parts = append(parts, item.Text.Content)
		}
	}
	return strings.Join(parts, "")
}

func richTextRequest(content string) []map[string]interface{} {
	content = strings.TrimSpace(content)
	if content == "" {
		return []map[string]interface{}{}
	}
	return []map[string]interface{}{
		{
			"type": "text",
			"text": map[string]interface{}{
				"content": content,
			},
		},
	}
}
