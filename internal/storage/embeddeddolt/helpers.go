//go:build embeddeddolt

package embeddeddolt

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

// insertIssueIntoTable inserts an issue into the specified table ("issues" or "wisps"),
// using ON DUPLICATE KEY UPDATE to handle pre-existing records gracefully.
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func insertIssueIntoTable(ctx context.Context, tx *sql.Tx, table string, issue *types.Issue) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, external_ref, spec_id,
			compaction_level, compacted_at, compacted_at_commit, original_size,
			sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
			mol_type, work_type, quality_score, source_system, source_repo, close_reason,
			event_kind, actor, target, payload,
			await_type, await_id, timeout_ns, waiters,
			hook_bead, role_bead, agent_state, last_activity, role_type, rig,
			due_at, defer_until, metadata
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
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
			closed_at = VALUES(closed_at),
			external_ref = VALUES(external_ref),
			source_repo = VALUES(source_repo),
			close_reason = VALUES(close_reason),
			metadata = VALUES(metadata)
	`, table),
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		issue.Status, issue.Priority, issue.IssueType, nullString(issue.Assignee), nullInt(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt, nullStringPtr(issue.ExternalRef), issue.SpecID,
		issue.CompactionLevel, issue.CompactedAt, nullStringPtr(issue.CompactedAtCommit), nullIntVal(issue.OriginalSize),
		issue.Sender, issue.Ephemeral, issue.WispType, issue.Pinned, issue.IsTemplate, issue.Crystallizes,
		issue.MolType, issue.WorkType, issue.QualityScore, issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONStringArray(issue.Waiters),
		issue.HookBead, issue.RoleBead, issue.AgentState, issue.LastActivity, issue.RoleType, issue.Rig,
		issue.DueAt, issue.DeferUntil, jsonMetadata(issue.Metadata),
	)
	if err != nil {
		return fmt.Errorf("insert issue into %s: %w", table, err)
	}
	return nil
}

// recordEventInTable records an event in the specified events table.
//
//nolint:gosec // G201: table is a hardcoded constant ("events" or "wisp_events")
func recordEventInTable(ctx context.Context, tx *sql.Tx, table, issueID string, eventType types.EventType, actor, newValue string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, table), issueID, eventType, actor, "", newValue)
	if err != nil {
		return fmt.Errorf("record event in %s: %w", table, err)
	}
	return nil
}

// generateIssueIDInTable generates a unique ID, checking for collisions
// in the specified table. Supports counter mode for non-ephemeral issues.
//
//nolint:gosec // G201: table is a hardcoded constant
func generateIssueIDInTable(ctx context.Context, tx *sql.Tx, table, prefix string, issue *types.Issue, actor string) (string, error) {
	// Counter mode only applies to the issues table (not wisps).
	if table == "issues" {
		counterMode, err := isCounterModeTx(ctx, tx)
		if err != nil {
			return "", err
		}
		if counterMode {
			return nextCounterIDTx(ctx, tx, prefix)
		}
	}

	// Default hash-based ID generation
	baseLength, err := getAdaptiveIDLengthTx(ctx, tx, table, prefix)
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

// isCounterModeTx checks whether issue_id_mode=counter is configured.
func isCounterModeTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var idMode string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_id_mode").Scan(&idMode)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("failed to read issue_id_mode config: %w", err)
	}
	return idMode == "counter", nil
}

// nextCounterIDTx atomically increments and returns the next sequential issue ID.
func nextCounterIDTx(ctx context.Context, tx *sql.Tx, prefix string) (string, error) {
	res, err := tx.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
	if err != nil {
		return "", fmt.Errorf("failed to increment issue counter for prefix %q: %w", prefix, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to check rows affected for issue counter prefix %q: %w", prefix, err)
	}

	if rowsAffected == 0 {
		if seedErr := seedCounterFromExistingIssuesTx(ctx, tx, prefix); seedErr != nil {
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

// seedCounterFromExistingIssuesTx scans existing issues to find the highest numeric suffix
// for the given prefix, then seeds the issue_counter table if no row exists yet.
func seedCounterFromExistingIssuesTx(ctx context.Context, tx *sql.Tx, prefix string) error {
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

// getAdaptiveIDLengthTx returns the appropriate hash length based on database size.
//
//nolint:gosec // G201: table is a hardcoded constant
func getAdaptiveIDLengthTx(ctx context.Context, tx *sql.Tx, table, prefix string) (int, error) {
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

	cfg := getAdaptiveConfigTx(ctx, tx)
	return computeAdaptiveLength(count, cfg), nil
}

type adaptiveIDConfig struct {
	maxCollisionProbability float64
	minLength               int
	maxLength               int
}

func defaultAdaptiveConfig() adaptiveIDConfig {
	return adaptiveIDConfig{
		maxCollisionProbability: 0.25,
		minLength:               3,
		maxLength:               8,
	}
}

func getAdaptiveConfigTx(ctx context.Context, tx *sql.Tx) adaptiveIDConfig {
	cfg := defaultAdaptiveConfig()

	var probStr string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "max_collision_prob").Scan(&probStr)
	if err == nil && probStr != "" {
		if prob, err := strconv.ParseFloat(probStr, 64); err == nil {
			cfg.maxCollisionProbability = prob
		}
	}

	var minLenStr string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "min_hash_length").Scan(&minLenStr)
	if err == nil && minLenStr != "" {
		if minLen, err := strconv.Atoi(minLenStr); err == nil {
			cfg.minLength = minLen
		}
	}

	var maxLenStr string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "max_hash_length").Scan(&maxLenStr)
	if err == nil && maxLenStr != "" {
		if maxLen, err := strconv.Atoi(maxLenStr); err == nil {
			cfg.maxLength = maxLen
		}
	}

	return cfg
}

func computeAdaptiveLength(numIssues int, cfg adaptiveIDConfig) int {
	const base = 36.0
	for length := cfg.minLength; length <= cfg.maxLength; length++ {
		totalPossibilities := math.Pow(base, float64(length))
		exponent := -float64(numIssues*numIssues) / (2.0 * totalPossibilities)
		prob := 1.0 - math.Exp(exponent)
		if prob <= cfg.maxCollisionProbability {
			return length
		}
	}
	return cfg.maxLength
}

// getCustomStatusesTx reads custom statuses from config within a transaction.
func getCustomStatusesTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "custom_statuses").Scan(&raw)
	if err == sql.ErrNoRows || raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read custom_statuses config: %w", err)
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

// getCustomTypesTx reads custom types from config within a transaction.
func getCustomTypesTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "custom_types").Scan(&raw)
	if err == sql.ErrNoRows || raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read custom_types config: %w", err)
	}
	var types []string
	if err := json.Unmarshal([]byte(raw), &types); err != nil {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				types = append(types, s)
			}
		}
	}
	return types, nil
}

// validateMetadataIfConfigured checks metadata against the schema from config.
func validateMetadataIfConfigured(metadata json.RawMessage) error {
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
		schema := parseFieldSchema(fieldMap)
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

// parseFieldSchema converts a raw config map into a MetadataFieldSchema.
func parseFieldSchema(m map[string]interface{}) storage.MetadataFieldSchema {
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

// isDoltNothingToCommit returns true if the error is the benign
// "nothing to commit" Dolt message.
func isDoltNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nothing to commit") ||
		(strings.Contains(s, "no changes") && strings.Contains(s, "commit"))
}

// ---------------------------------------------------------------------------
// Nullable value helpers
// ---------------------------------------------------------------------------

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullStringPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func nullInt(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

func nullIntVal(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

func jsonMetadata(m []byte) string {
	if len(m) == 0 {
		return "{}"
	}
	if !json.Valid(m) {
		fmt.Fprintf(os.Stderr, "Warning: invalid JSON metadata, using empty object\n")
		return "{}"
	}
	return string(m)
}

func formatJSONStringArray(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	data, err := json.Marshal(arr)
	if err != nil {
		return ""
	}
	return string(data)
}
