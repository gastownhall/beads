// Package issueops provides shared transaction-scoped SQL operations for
// issue creation and management. Both DoltStore and EmbeddedDoltStore call
// into these functions, passing their own *sql.Tx obtained through their
// respective connection lifecycle patterns.
package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IsWisp returns true if the issue should be routed to the wisps table.
// Routes based on flags only — not the ID pattern. The "-wisp-" ID prefix is
// a naming convention for generated wisp IDs, but promoted wisps keep their
// ID while moving to the issues table (Ephemeral=false). Routing on the ID
// would send promoted wisps back to the wisps table on re-insert.
func IsWisp(issue *types.Issue) bool {
	return issue.Ephemeral || issue.NoHistory
}

// TableRouting returns the issue and event table names for an issue,
// routing ephemeral issues to the wisps tables.
func TableRouting(issue *types.Issue) (issueTable, eventTable string) {
	if IsWisp(issue) {
		return "wisps", "wisp_events"
	}
	return "issues", "events"
}

// InsertIssueIntoTable inserts an issue into the specified table ("issues" or "wisps"),
// using ON DUPLICATE KEY UPDATE to handle pre-existing records gracefully.
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func InsertIssueIntoTable(ctx context.Context, tx *sql.Tx, table string, issue *types.Issue) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, started_at, closed_at, external_ref, spec_id,
			compaction_level, compacted_at, compacted_at_commit, original_size,
			sender, ephemeral, no_history, wisp_type, pinned, is_template,
			mol_type, work_type, source_system, source_repo, close_reason,
			event_kind, actor, target, payload,
			await_type, await_id, timeout_ns, waiters,
			due_at, defer_until, metadata
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?
		)
		ON DUPLICATE KEY UPDATE
			content_hash = VALUES(content_hash),
			title = VALUES(title),
			description = VALUES(description),
			design = VALUES(design),
			acceptance_criteria = VALUES(acceptance_criteria),
			notes = VALUES(notes),
			status = VALUES(status),
			priority = VALUES(priority),
			issue_type = VALUES(issue_type),
			assignee = VALUES(assignee),
			estimated_minutes = VALUES(estimated_minutes),
			updated_at = VALUES(updated_at),
			started_at = VALUES(started_at),
			closed_at = VALUES(closed_at),
			external_ref = VALUES(external_ref),
			source_repo = VALUES(source_repo),
			close_reason = VALUES(close_reason),
			metadata = VALUES(metadata)
	`, table),
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		issue.Status, issue.Priority, issue.IssueType, NullString(issue.Assignee), NullInt(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.StartedAt, issue.ClosedAt, NullStringPtr(issue.ExternalRef), issue.SpecID,
		issue.CompactionLevel, issue.CompactedAt, NullStringPtr(issue.CompactedAtCommit), NullIntVal(issue.OriginalSize),
		issue.Sender, issue.Ephemeral, issue.NoHistory, issue.WispType, issue.Pinned, issue.IsTemplate,
		issue.MolType, issue.WorkType, issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), FormatJSONStringArray(issue.Waiters),
		issue.DueAt, issue.DeferUntil, JSONMetadata(issue.Metadata),
	)
	if err != nil {
		return fmt.Errorf("insert issue into %s: %w", table, err)
	}
	return nil
}

// RecordEventInTable records an event in the specified events table.
//
//nolint:gosec // G201: table is a hardcoded constant ("events" or "wisp_events")
func RecordEventInTable(ctx context.Context, tx *sql.Tx, table, issueID string, eventType types.EventType, actor, newValue string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, table), issueID, eventType, actor, "", newValue)
	if err != nil {
		return fmt.Errorf("record event in %s: %w", table, err)
	}
	return nil
}

// GenerateIssueIDInTable generates a unique ID, checking for collisions
// in the specified table. Supports counter mode for non-ephemeral issues.
//
//nolint:gosec // G201: table is a hardcoded constant
func GenerateIssueIDInTable(ctx context.Context, tx *sql.Tx, table, prefix string, issue *types.Issue, actor string) (string, error) {
	// Counter mode only applies to the issues table (not wisps).
	if table == "issues" {
		counterMode, err := IsCounterModeTx(ctx, tx)
		if err != nil {
			return "", err
		}
		if counterMode {
			return NextCounterIDTx(ctx, tx, prefix)
		}
	}

	// Default hash-based ID generation
	baseLength, err := GetAdaptiveIDLengthTx(ctx, tx, table, prefix)
	if err != nil {
		baseLength = 6
	}

	maxLength := 8
	if baseLength > maxLength {
		baseLength = maxLength
	}

	for length := baseLength; length <= maxLength; length++ {
		for nonce := 0; nonce < 10; nonce++ {
			candidate := idgen.GenerateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)

			var count int
			err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, table), candidate).Scan(&count)
			if err != nil {
				return "", fmt.Errorf("failed to check for ID collision: %w", err)
			}

			if count == 0 {
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("failed to generate unique ID after trying lengths %d-%d with 10 nonces each", baseLength, maxLength)
}

// IsCounterModeTx checks whether issue_id_mode=counter is configured.
func IsCounterModeTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var idMode string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_id_mode").Scan(&idMode)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("failed to read issue_id_mode config: %w", err)
	}
	return idMode == "counter", nil
}

// NextCounterIDTx atomically increments and returns the next sequential issue ID.
func NextCounterIDTx(ctx context.Context, tx *sql.Tx, prefix string) (string, error) {
	res, err := tx.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
	if err != nil {
		return "", fmt.Errorf("failed to increment issue counter for prefix %q: %w", prefix, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to check rows affected for issue counter prefix %q: %w", prefix, err)
	}

	if rowsAffected == 0 {
		if seedErr := SeedCounterFromExistingIssuesTx(ctx, tx, prefix); seedErr != nil {
			return "", fmt.Errorf("failed to seed issue counter for prefix %q: %w", prefix, seedErr)
		}
		res, err = tx.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
		if err != nil {
			return "", fmt.Errorf("failed to increment issue counter after seeding for prefix %q: %w", prefix, err)
		}
		rowsAffected, err = res.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("failed to check rows affected after seeding for prefix %q: %w", prefix, err)
		}
		if rowsAffected == 0 {
			_, err = tx.ExecContext(ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES (?, 1)", prefix)
			if err != nil {
				return "", fmt.Errorf("failed to insert initial issue counter for prefix %q: %w", prefix, err)
			}
		}
	}

	var nextID int
	err = tx.QueryRowContext(ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix).Scan(&nextID)
	if err != nil {
		return "", fmt.Errorf("failed to read issue counter after increment for prefix %q: %w", prefix, err)
	}
	return fmt.Sprintf("%s-%d", prefix, nextID), nil
}

// SeedCounterFromExistingIssuesTx scans existing issues to find the highest numeric suffix
// for the given prefix, then seeds the issue_counter table if no row exists yet.
func SeedCounterFromExistingIssuesTx(ctx context.Context, tx *sql.Tx, prefix string) error {
	var existing int
	err := tx.QueryRowContext(ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix).Scan(&existing)
	if err == nil {
		return nil // already seeded
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("failed to check existing counter for prefix %q: %w", prefix, err)
	}

	// Find max numeric suffix among existing issues
	rows, err := tx.QueryContext(ctx, `SELECT id FROM issues WHERE id LIKE CONCAT(?, '-%')`, prefix)
	if err != nil {
		return fmt.Errorf("failed to scan existing issues for prefix %q: %w", prefix, err)
	}
	defer rows.Close()

	maxNum := 0
	pfxDash := prefix + "-"
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		suffix := strings.TrimPrefix(id, pfxDash)
		if strings.Contains(suffix, ".") {
			continue // skip child IDs
		}
		if n, err := strconv.Atoi(suffix); err == nil && n > maxNum {
			maxNum = n
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate issues for prefix %q: %w", prefix, err)
	}

	if maxNum > 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES (?, ?)", prefix, maxNum)
		if err != nil {
			return fmt.Errorf("failed to seed issue counter for prefix %q at %d: %w", prefix, maxNum, err)
		}
	}
	return nil
}

// GetAdaptiveIDLengthTx returns the appropriate hash length based on database size.
//
//nolint:gosec // G201: table is a hardcoded constant
func GetAdaptiveIDLengthTx(ctx context.Context, tx *sql.Tx, table, prefix string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s
		WHERE id LIKE CONCAT(?, '-%%')
		  AND INSTR(SUBSTRING(id, LENGTH(?) + 2), '.') = 0
	`, table), prefix, prefix).Scan(&count)
	if err != nil {
		return 6, err
	}

	cfg := GetAdaptiveConfigTx(ctx, tx)
	return ComputeAdaptiveLength(count, cfg), nil
}

// AdaptiveIDConfig holds configuration for adaptive ID length computation.
type AdaptiveIDConfig struct {
	MaxCollisionProbability float64
	MinLength               int
	MaxLength               int
}

// DefaultAdaptiveConfig returns the default adaptive ID configuration.
func DefaultAdaptiveConfig() AdaptiveIDConfig {
	return AdaptiveIDConfig{
		MaxCollisionProbability: 0.25,
		MinLength:               3,
		MaxLength:               8,
	}
}

// GetAdaptiveConfigTx reads adaptive ID config from the database.
func GetAdaptiveConfigTx(ctx context.Context, tx *sql.Tx) AdaptiveIDConfig {
	cfg := DefaultAdaptiveConfig()

	var probStr string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "max_collision_prob").Scan(&probStr)
	if err == nil && probStr != "" {
		if prob, err := strconv.ParseFloat(probStr, 64); err == nil {
			cfg.MaxCollisionProbability = prob
		}
	}

	var minLenStr string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "min_hash_length").Scan(&minLenStr)
	if err == nil && minLenStr != "" {
		if minLen, err := strconv.Atoi(minLenStr); err == nil {
			cfg.MinLength = minLen
		}
	}

	var maxLenStr string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "max_hash_length").Scan(&maxLenStr)
	if err == nil && maxLenStr != "" {
		if maxLen, err := strconv.Atoi(maxLenStr); err == nil {
			cfg.MaxLength = maxLen
		}
	}

	return cfg
}

// ComputeAdaptiveLength uses the birthday paradox to pick a hash length
// that keeps collision probability below the configured threshold.
func ComputeAdaptiveLength(numIssues int, cfg AdaptiveIDConfig) int {
	const base = 36.0
	for length := cfg.MinLength; length <= cfg.MaxLength; length++ {
		totalPossibilities := math.Pow(base, float64(length))
		exponent := -float64(numIssues*numIssues) / (2.0 * totalPossibilities)
		prob := 1.0 - math.Exp(exponent)
		if prob <= cfg.MaxCollisionProbability {
			return length
		}
	}
	return cfg.MaxLength
}

// GetCustomStatusesTx reads custom statuses from config within a transaction.
func GetCustomStatusesTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "status.custom").Scan(&raw)
	if err == sql.ErrNoRows || raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read status.custom config: %w", err)
	}
	var statuses []string
	if err := json.Unmarshal([]byte(raw), &statuses); err != nil {
		// Try comma-separated fallback
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				statuses = append(statuses, s)
			}
		}
	}
	return statuses, nil
}

// GetCustomTypesTx reads custom types from config within a transaction.
func GetCustomTypesTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "types.custom").Scan(&raw)
	if err == sql.ErrNoRows || raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read custom_types config: %w", err)
	}
	var customTypes []string
	if err := json.Unmarshal([]byte(raw), &customTypes); err != nil {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				customTypes = append(customTypes, s)
			}
		}
	}
	return customTypes, nil
}

// ValidateMetadataIfConfigured checks metadata against the schema from config.
func ValidateMetadataIfConfigured(metadata json.RawMessage) error {
	mode := config.MetadataValidationMode()
	if mode == "none" || mode == "" {
		return nil
	}

	rawFields := config.MetadataSchemaFields()
	if rawFields == nil {
		return nil
	}

	fields := make(map[string]storage.MetadataFieldSchema)
	for name, raw := range rawFields {
		fieldMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		schema := ParseFieldSchema(fieldMap)
		fields[name] = schema
	}

	if len(fields) == 0 {
		return nil
	}

	schemaCfg := storage.MetadataSchemaConfig{
		Mode:   mode,
		Fields: fields,
	}

	errs := storage.ValidateMetadataSchema(metadata, schemaCfg)
	if len(errs) == 0 {
		return nil
	}

	if mode == "warn" {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "warning: %s\n", e.Error())
		}
		return nil
	}

	return fmt.Errorf("metadata schema violation: %s", errs[0].Error())
}

// ParseFieldSchema converts a raw config map into a MetadataFieldSchema.
func ParseFieldSchema(m map[string]interface{}) storage.MetadataFieldSchema {
	schema := storage.MetadataFieldSchema{}

	if t, ok := m["type"].(string); ok {
		schema.Type = storage.MetadataFieldType(t)
	}
	if req, ok := m["required"].(bool); ok {
		schema.Required = req
	}

	if vals, ok := m["values"]; ok {
		switch v := vals.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					schema.Values = append(schema.Values, s)
				}
			}
		case string:
			for _, s := range strings.Split(v, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					schema.Values = append(schema.Values, s)
				}
			}
		}
	}

	if min, ok := toFloat64(m["min"]); ok {
		schema.Min = &min
	}
	if max, ok := toFloat64(m["max"]); ok {
		schema.Max = &max
	}

	return schema
}

func toFloat64(v interface{}) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// IsDoltNothingToCommit returns true if the error is the benign
// "nothing to commit" Dolt message.
func IsDoltNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nothing to commit") ||
		(strings.Contains(s, "no changes") && strings.Contains(s, "commit"))
}

// ReadConfigPrefix reads and normalizes issue_prefix from the config table.
func ReadConfigPrefix(ctx context.Context, tx *sql.Tx) (string, error) {
	var configPrefix string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return "", fmt.Errorf("%w: issue_prefix config is missing (run 'bd init --prefix <prefix>' for a new project, or 'bd bootstrap' to clone an existing remote)", storage.ErrNotInitialized)
	} else if err != nil {
		return "", fmt.Errorf("failed to get config: %w", err)
	}
	return strings.TrimSuffix(configPrefix, "-"), nil
}

// ---------------------------------------------------------------------------
// Nullable value helpers
// ---------------------------------------------------------------------------

// NullString returns nil for empty strings, otherwise the string value.
func NullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// NullStringPtr returns nil for nil pointers, otherwise the pointed-to string.
func NullStringPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

// NullInt returns nil for nil pointers, otherwise the pointed-to int.
func NullInt(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

// NullIntVal returns nil for zero values, otherwise the int.
func NullIntVal(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

// JSONMetadata returns the metadata as a JSON string, defaulting to "{}".
func JSONMetadata(m []byte) string {
	if len(m) == 0 {
		return "{}"
	}
	if !json.Valid(m) {
		fmt.Fprintf(os.Stderr, "Warning: invalid JSON metadata, using empty object\n")
		return "{}"
	}
	return string(m)
}

// FormatJSONStringArray marshals a string slice to JSON, returning "" for empty/nil.
func FormatJSONStringArray(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	data, err := json.Marshal(arr)
	if err != nil {
		return ""
	}
	return string(data)
}
