package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

const BeadsSchemaVersion = "beads/v1"

type BeadsConfig struct {
	DatabaseID    string `json:"database_id"`
	DataSourceID  string `json:"data_source_id"`
	ViewURL       string `json:"view_url"`
	SchemaVersion string `json:"schema_version"`
}

type ManagedIssueSnapshot struct {
	ID           string   `json:"id,omitempty"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty"`
	Status       string   `json:"status,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	IssueType    string   `json:"issue_type,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	ExternalRef  string   `json:"external_ref,omitempty"`
	NotionPageID string   `json:"notion_page_id,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

type BeadsStateEntry struct {
	PageID         string                `json:"page_id"`
	CachedIssue    *ManagedIssueSnapshot `json:"cached_issue,omitempty"`
	LastVerifiedAt string                `json:"last_verified_at,omitempty"`
}

type BeadsState struct {
	DatabaseID string                     `json:"database_id"`
	Entries    map[string]BeadsStateEntry `json:"entries,omitempty"`
	PageIDs    map[string]string          `json:"-"`
}

func EmptyBeadsState(databaseID string) *BeadsState {
	return &BeadsState{
		DatabaseID: strings.TrimSpace(databaseID),
		Entries:    map[string]BeadsStateEntry{},
		PageIDs:    map[string]string{},
	}
}

type beadsStateJSON struct {
	DatabaseID string                     `json:"database_id"`
	Entries    map[string]BeadsStateEntry `json:"entries,omitempty"`
	PageIDs    map[string]string          `json:"page_ids,omitempty"`
}

func (s *BeadsState) UnmarshalJSON(data []byte) error {
	var raw beadsStateJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.DatabaseID = raw.DatabaseID
	s.Entries = cloneEntries(raw.Entries)
	s.PageIDs = clonePageIDs(raw.PageIDs)
	if len(s.Entries) == 0 && len(s.PageIDs) > 0 {
		s.Entries = entriesFromPageIDs(s.PageIDs)
	}
	if len(s.PageIDs) == 0 && len(s.Entries) > 0 {
		s.PageIDs = pageIDsFromEntries(s.Entries)
	}
	return nil
}

func (s BeadsState) MarshalJSON() ([]byte, error) {
	norm, err := NormalizeBeadsState(&s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(beadsStateJSON{DatabaseID: norm.DatabaseID, Entries: norm.Entries})
}

func (s *BeadsState) IDs() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, 0, len(s.PageIDs))
	for id := range s.PageIDs {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	return keys
}

func (s *BeadsState) PageIDFor(id string) string {
	if s == nil {
		return ""
	}
	return s.PageIDs[strings.TrimSpace(id)]
}

func (s *BeadsState) EntryFor(id string) (BeadsStateEntry, bool) {
	if s == nil {
		return BeadsStateEntry{}, false
	}
	entry, ok := s.Entries[strings.TrimSpace(id)]
	return entry, ok
}

func (s *BeadsState) SetEntry(id string, entry BeadsStateEntry) {
	if s == nil {
		return
	}
	if s.Entries == nil {
		s.Entries = map[string]BeadsStateEntry{}
	}
	if s.PageIDs == nil {
		s.PageIDs = map[string]string{}
	}
	id = strings.TrimSpace(id)
	entry.PageID = strings.TrimSpace(entry.PageID)
	entry.LastVerifiedAt = strings.TrimSpace(entry.LastVerifiedAt)
	entry.CachedIssue = normalizeSnapshot(entry.CachedIssue)
	s.Entries[id] = entry
	s.PageIDs[id] = entry.PageID
}

func (s *BeadsState) SetPageID(id, pageID string) {
	if s == nil {
		return
	}
	entry, _ := s.EntryFor(id)
	entry.PageID = pageID
	s.SetEntry(id, entry)
}

func (s *BeadsState) Delete(id string) {
	if s == nil {
		return
	}
	id = strings.TrimSpace(id)
	delete(s.Entries, id)
	delete(s.PageIDs, id)
}

func ReadBeadsConfig(paths *Paths) (*BeadsConfig, error) {
	var cfg BeadsConfig
	ok, err := readJSON(paths.BeadsConfigPath, &cfg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if err := ValidateBeadsConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveBeadsConfig(paths *Paths, cfg *BeadsConfig) error {
	if err := ValidateBeadsConfig(cfg); err != nil {
		return err
	}
	return writeJSON0600(paths.BeadsConfigPath, cfg)
}

func ClearBeadsConfig(paths *Paths) (bool, error) {
	err := os.Remove(paths.BeadsConfigPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func ValidateBeadsConfig(cfg *BeadsConfig) error {
	if cfg == nil {
		return fmt.Errorf("beads config is nil")
	}
	if strings.TrimSpace(cfg.DatabaseID) == "" || strings.TrimSpace(cfg.DataSourceID) == "" || strings.TrimSpace(cfg.ViewURL) == "" || strings.TrimSpace(cfg.SchemaVersion) == "" {
		return fmt.Errorf("beads config requires database_id, data_source_id, view_url, and schema_version")
	}
	return nil
}

func ReadBeadsState(paths *Paths) (*BeadsState, error) {
	var st BeadsState
	ok, err := readJSON(paths.BeadsStatePath, &st)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	norm, err := NormalizeBeadsState(&st)
	if err != nil {
		return nil, err
	}
	return norm, nil
}

func SaveBeadsState(paths *Paths, st *BeadsState) error {
	norm, err := NormalizeBeadsState(st)
	if err != nil {
		return err
	}
	return writeJSON0600(paths.BeadsStatePath, norm)
}

func SaveBeadsTarget(paths *Paths, cfg *BeadsConfig) error {
	if err := ValidateBeadsConfig(cfg); err != nil {
		return err
	}
	previousCfg, err := ReadBeadsConfig(paths)
	if err != nil {
		return err
	}
	previousState, err := ReadBeadsState(paths)
	if err != nil {
		return err
	}
	if err := SaveBeadsConfig(paths, cfg); err != nil {
		return err
	}
	if err := SaveBeadsState(paths, EmptyBeadsState(cfg.DatabaseID)); err != nil {
		if rollbackErr := restoreBeadsTarget(paths, previousCfg, previousState); rollbackErr != nil {
			return fmt.Errorf("save beads state: %w (rollback failed: %v)", err, rollbackErr)
		}
		return err
	}
	return nil
}

func ClearBeadsState(paths *Paths) (bool, error) {
	err := os.Remove(paths.BeadsStatePath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func NormalizeBeadsState(st *BeadsState) (*BeadsState, error) {
	if st == nil {
		return nil, fmt.Errorf("beads state is nil")
	}
	if strings.TrimSpace(st.DatabaseID) == "" {
		return nil, fmt.Errorf("beads state requires database_id")
	}
	rawEntries := cloneEntries(st.Entries)
	if len(rawEntries) == 0 && len(st.PageIDs) > 0 {
		rawEntries = entriesFromPageIDs(st.PageIDs)
	}
	if len(rawEntries) == 0 {
		return EmptyBeadsState(st.DatabaseID), nil
	}
	for id, pageID := range st.PageIDs {
		trimmedID := strings.TrimSpace(id)
		if _, ok := rawEntries[trimmedID]; ok {
			continue
		}
		rawEntries[trimmedID] = BeadsStateEntry{PageID: pageID}
	}
	keys := make([]string, 0, len(rawEntries))
	for id := range rawEntries {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	normalized := &BeadsState{
		DatabaseID: strings.TrimSpace(st.DatabaseID),
		Entries:    make(map[string]BeadsStateEntry, len(rawEntries)),
		PageIDs:    make(map[string]string, len(rawEntries)),
	}
	seen := map[string]string{}
	for _, id := range keys {
		trimmedID := strings.TrimSpace(id)
		if trimmedID == "" {
			return nil, fmt.Errorf("beads state contains an empty Beads ID key")
		}
		entry := rawEntries[id]
		pageID := strings.TrimSpace(entry.PageID)
		if pageID == "" {
			return nil, fmt.Errorf("beads state contains an empty page id for Beads ID %s", trimmedID)
		}
		if existing, ok := seen[pageID]; ok {
			return nil, fmt.Errorf("beads state contains duplicate page id %s for %s and %s", pageID, existing, trimmedID)
		}
		seen[pageID] = trimmedID
		normalizedEntry := BeadsStateEntry{
			PageID:         pageID,
			CachedIssue:    normalizeSnapshot(entry.CachedIssue),
			LastVerifiedAt: strings.TrimSpace(entry.LastVerifiedAt),
		}
		normalized.Entries[trimmedID] = normalizedEntry
		normalized.PageIDs[trimmedID] = pageID
	}
	return normalized, nil
}

func ValidateBeadsState(st *BeadsState) error {
	_, err := NormalizeBeadsState(st)
	return err
}

func ImportBeadsState(paths *Paths, r io.Reader, cfg *BeadsConfig) (*BeadsState, error) {
	var st BeadsState
	if err := json.NewDecoder(r).Decode(&st); err != nil {
		return nil, err
	}
	if cfg != nil && strings.TrimSpace(cfg.DatabaseID) != "" && strings.TrimSpace(st.DatabaseID) == "" {
		st.DatabaseID = cfg.DatabaseID
	}
	norm, err := NormalizeBeadsState(&st)
	if err != nil {
		return nil, err
	}
	if cfg != nil && strings.TrimSpace(cfg.DatabaseID) != "" && norm.DatabaseID != cfg.DatabaseID {
		return nil, fmt.Errorf("beads state targets database %s, but saved config expects %s", norm.DatabaseID, cfg.DatabaseID)
	}
	if err := SaveBeadsState(paths, norm); err != nil {
		return nil, err
	}
	return norm, nil
}

func restoreBeadsTarget(paths *Paths, cfg *BeadsConfig, st *BeadsState) error {
	if cfg == nil {
		if _, err := ClearBeadsConfig(paths); err != nil {
			return err
		}
	} else if err := SaveBeadsConfig(paths, cfg); err != nil {
		return err
	}
	if st == nil {
		if _, err := ClearBeadsState(paths); err != nil {
			return err
		}
		return nil
	}
	return SaveBeadsState(paths, st)
}

func cloneEntries(entries map[string]BeadsStateEntry) map[string]BeadsStateEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]BeadsStateEntry, len(entries))
	for id, entry := range entries {
		out[id] = BeadsStateEntry{
			PageID:         strings.TrimSpace(entry.PageID),
			CachedIssue:    normalizeSnapshot(entry.CachedIssue),
			LastVerifiedAt: strings.TrimSpace(entry.LastVerifiedAt),
		}
	}
	return out
}

func clonePageIDs(pageIDs map[string]string) map[string]string {
	if len(pageIDs) == 0 {
		return nil
	}
	out := make(map[string]string, len(pageIDs))
	for id, pageID := range pageIDs {
		out[strings.TrimSpace(id)] = strings.TrimSpace(pageID)
	}
	return out
}

func entriesFromPageIDs(pageIDs map[string]string) map[string]BeadsStateEntry {
	if len(pageIDs) == 0 {
		return nil
	}
	out := make(map[string]BeadsStateEntry, len(pageIDs))
	for id, pageID := range pageIDs {
		out[strings.TrimSpace(id)] = BeadsStateEntry{PageID: strings.TrimSpace(pageID)}
	}
	return out
}

func pageIDsFromEntries(entries map[string]BeadsStateEntry) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for id, entry := range entries {
		out[strings.TrimSpace(id)] = strings.TrimSpace(entry.PageID)
	}
	return out
}

func normalizeSnapshot(snapshot *ManagedIssueSnapshot) *ManagedIssueSnapshot {
	if snapshot == nil {
		return nil
	}
	copy := *snapshot
	copy.ID = strings.TrimSpace(copy.ID)
	copy.Title = strings.TrimSpace(copy.Title)
	copy.Description = strings.TrimSpace(copy.Description)
	copy.Status = strings.TrimSpace(copy.Status)
	copy.Priority = strings.TrimSpace(copy.Priority)
	copy.IssueType = strings.TrimSpace(copy.IssueType)
	copy.Assignee = strings.TrimSpace(copy.Assignee)
	copy.Labels = normalizeLabels(copy.Labels)
	copy.ExternalRef = strings.TrimSpace(copy.ExternalRef)
	copy.NotionPageID = strings.TrimSpace(copy.NotionPageID)
	copy.CreatedAt = strings.TrimSpace(copy.CreatedAt)
	copy.UpdatedAt = strings.TrimSpace(copy.UpdatedAt)
	return &copy
}

func normalizeLabels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
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
