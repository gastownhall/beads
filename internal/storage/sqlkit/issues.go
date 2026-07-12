package sqlkit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// CreateIssue creates a new issue. Wisp routing (ephemeral / no-history / infra
// type) is resolved inside the tx; issueops picks the wisps table from there.
func (s *Store) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("issue must not be nil")
	}
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		// Route to wisps if ephemeral, no-history, or an infra type. Infra types
		// get marked ephemeral (legacy behavior); TableRouting inside issueops
		// then selects the wisps table.
		useWisps := issue.Ephemeral || issue.NoHistory ||
			issueops.ResolveInfraTypesInTx(ctx, tx)[string(issue.IssueType)]
		if useWisps && !issue.NoHistory {
			issue.Ephemeral = true
		}
		// SkipPrefixValidation matches legacy: the single-issue path never
		// prefix-validates explicit IDs.
		bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		})
		if err != nil {
			return err
		}
		_, err = issueops.CreateIssueInTxWithResult(ctx, tx, bc, issue, actor)
		return err
	})
}

// CreateIssues creates multiple issues in a single transaction. issueops routes
// mixed batches per issue and validates cross-bucket dependencies internally.
func (s *Store) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if len(issues) == 0 {
		return nil
	}
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		_, err := issueops.CreateIssuesInTxWithResult(ctx, tx, issues, actor, storage.BatchCreateOptions{
			OrphanHandling:       storage.OrphanAllow,
			SkipPrefixValidation: false,
		})
		return err
	})
}

// GetIssue retrieves an issue by ID. Returns storage.ErrNotFound (wrapped) when
// the issue does not exist.
func (s *Store) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var issue *types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		issue, e = issueops.GetIssueInTx(ctx, tx, id)
		return e
	})
	return issue, err
}

// GetIssueByExternalRef retrieves an issue by its external reference, spanning both
// the issues and wisps tiers. Returns a wrapped storage.ErrNotFound when none matches.
// Resolves the ID in a read tx (issueops), then reuses GetIssue so wisp-tier hydration
// and not-found semantics are identical to a normal fetch.
func (s *Store) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	var id string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		id, e = issueops.GetIssueByExternalRefInTx(ctx, tx, externalRef)
		return e
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// GetIssuesByIDs retrieves multiple issues by ID, spanning both the issues and
// wisps tiers.
func (s *Store) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetIssuesByIDsInTx(ctx, tx, ids, nil)
		return e
	})
	return out, err
}

// UpdateIssue updates fields on an issue. Metadata is validated against the
// configured schema before delegation; wisp routing happens inside issueops.
func (s *Store) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Validate metadata against schema before delegation (GH#1416 Phase 2).
	if rawMeta, ok := updates["metadata"]; ok {
		metadataStr, err := storage.NormalizeMetadataValue(rawMeta)
		if err != nil {
			return fmt.Errorf("invalid metadata: %w", err)
		}
		if err := validateMetadataIfConfigured(json.RawMessage(metadataStr)); err != nil {
			return err
		}
	}

	// Setting no_history/wisp on a durable issue is an IN-PLACE column write on the
	// embedded-Dolt reference (the default backend + our oracle): updateIssueInTx routes
	// by IsActiveWispInTx and, for a durable id, updates the issues row in place — the row
	// is NOT migrated to the wisps table (only the server-Dolt store's DemoteToWisp does
	// that). Delegating to the same shared helper matches the reference exactly.
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		_, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor)
		return err
	})
}

// UpdateIssueType changes the issue_type field of an issue. Type validation
// happens inside UpdateIssueInTx via ResolveCustomTypesInTx.
func (s *Store) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	return s.UpdateIssue(ctx, id, map[string]interface{}{"issue_type": issueType}, actor)
}

// ReopenIssue reopens a closed issue: sets status=open, clears closed_at and
// defer_until, records EventReopened, adds the reason comment when non-empty,
// and recomputes is_blocked for affected IDs — all in one tx.
func (s *Store) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	// Mirror the embedded-Dolt reference exactly: reopen via UpdateIssue (which always
	// records a status event, bumps updated_at, and returns a wrapped storage.ErrNotFound
	// for a missing id — even when the issue is already open), then attach the reason as a
	// comment. The old issueops.ReopenIssueInTx path used WHERE status='closed', which made
	// reopening an already-open issue a silent no-op and returned a plain not-found error.
	updates := map[string]interface{}{
		"status":      string(types.StatusOpen),
		"defer_until": nil,
	}
	if err := s.UpdateIssue(ctx, id, updates, actor); err != nil {
		return err
	}
	if reason != "" {
		return s.AddComment(ctx, id, actor, reason)
	}
	return nil
}

// DeleteIssue permanently removes an issue. issueops routes wisps internally and
// recomputes is_blocked for affected neighbors.
func (s *Store) DeleteIssue(ctx context.Context, id string) error {
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		return issueops.DeleteIssueInTx(ctx, tx, id)
	})
}

// HeartbeatIssue renews the lease on a claimed issue. Mirrors embeddeddolt:
// wisps are ephemeral and never leased, so heartbeating one is ErrNotClaimable;
// otherwise delegate to issueops.HeartbeatIssueInTx.
func (s *Store) HeartbeatIssue(ctx context.Context, id, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			return fmt.Errorf("%w: %s is ephemeral", storage.ErrNotClaimable, id)
		}
		return issueops.HeartbeatIssueInTx(ctx, tx, id, actor)
	})
}

// ReclaimExpiredLeases reverts in_progress issues whose lease expired more than
// olderThan ago back to ready, recovering work stranded by dead workers. Mirrors
// embeddeddolt (issueops.ReclaimExpiredLeasesInTx does the shared SQL).
func (s *Store) ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	var reclaimed []types.ReclaimedLease
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		reclaimed, err = issueops.ReclaimExpiredLeasesInTx(ctx, tx, cutoff, actor)
		return err
	})
	return reclaimed, err
}

// --- metadata schema helpers (config-only; copied from dolt/metadata_schema.go) ---

// loadMetadataSchema reads the metadata validation config from YAML and
// returns a parsed schema. Returns mode "none" with empty fields if config
// is not initialized, mode is empty/unknown, or no fields are defined.
func loadMetadataSchema() storage.MetadataSchemaConfig {
	mode := config.MetadataValidationMode()
	if mode == "none" {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	rawFields := config.MetadataSchemaFields()
	if rawFields == nil {
		return storage.MetadataSchemaConfig{Mode: "none"}
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
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	return storage.MetadataSchemaConfig{
		Mode:   mode,
		Fields: fields,
	}
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

	// Parse enum values
	if vals, ok := m["values"]; ok {
		switch v := vals.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					schema.Values = append(schema.Values, s)
				}
			}
		case string:
			// Comma-separated fallback
			for _, s := range strings.Split(v, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					schema.Values = append(schema.Values, s)
				}
			}
		}
	}

	// Parse min/max for numeric types
	if min, ok := toFloat64(m["min"]); ok {
		schema.Min = &min
	}
	if max, ok := toFloat64(m["max"]); ok {
		schema.Max = &max
	}

	return schema
}

// toFloat64 converts an interface{} to float64, handling int and float YAML values.
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

// validateMetadataIfConfigured checks metadata against the schema from config.
// In "warn" mode, prints warnings to stderr and returns nil.
// In "error" mode, returns the first validation error.
// In "none" mode (or if config is not initialized), does nothing.
func validateMetadataIfConfigured(metadata json.RawMessage) error {
	schema := loadMetadataSchema()
	if schema.Mode == "none" {
		return nil
	}

	errs := storage.ValidateMetadataSchema(metadata, schema)
	if len(errs) == 0 {
		return nil
	}

	if schema.Mode == "warn" {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "warning: %s\n", e.Error())
		}
		return nil
	}

	// mode == "error"
	return fmt.Errorf("metadata schema violation: %s", errs[0].Error())
}
