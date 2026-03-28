package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// SlotSet atomically sets key=value in the issue's Metadata JSON object.
// Creates the metadata object if absent. Skips Dolt versioning for wisp issues.
func (s *DoltStore) SlotSet(ctx context.Context, id, key, value string) error {
	if err := storage.ValidateMetadataKey(key); err != nil {
		return err
	}
	isWisp := s.isActiveWisp(ctx, id)
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.SlotSetInTx(ctx, tx, id, key, value)
	}); err != nil {
		return err
	}
	if !isWisp {
		return s.doltAddAndCommit(ctx, []string{"issues"}, fmt.Sprintf("bd: slot-set %s %s", id, key))
	}
	return nil
}

// SlotGet reads a key from the issue's Metadata JSON object.
// Returns (value, true, nil) when the key exists, ("", false, nil) when absent.
func (s *DoltStore) SlotGet(ctx context.Context, id, key string) (string, bool, error) {
	if err := storage.ValidateMetadataKey(key); err != nil {
		return "", false, err
	}
	var val string
	var ok bool
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		val, ok, err = issueops.SlotGetInTx(ctx, tx, id, key)
		return err
	})
	return val, ok, err
}

// SlotClear removes a key from the issue's Metadata JSON object.
// No-op if the key is absent. Skips Dolt versioning for wisp issues.
func (s *DoltStore) SlotClear(ctx context.Context, id, key string) error {
	if err := storage.ValidateMetadataKey(key); err != nil {
		return err
	}
	isWisp := s.isActiveWisp(ctx, id)
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.SlotClearInTx(ctx, tx, id, key)
	}); err != nil {
		return err
	}
	if !isWisp {
		return s.doltAddAndCommit(ctx, []string{"issues"}, fmt.Sprintf("bd: slot-clear %s %s", id, key))
	}
	return nil
}
