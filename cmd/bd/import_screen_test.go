package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// countingVocabulary is the unit-test fake behind the import screen's
// vocabulary seam: it counts reads (the screen must read at most once, and
// never when no row needs the vocabulary) and can answer with an error.
type countingVocabulary struct {
	err   error
	reads int
}

func (v *countingVocabulary) ReadInsertVocabulary(context.Context) ([]string, []string, error) {
	v.reads++
	if v.err != nil {
		return nil, nil, v.err
	}
	return []string{"verify"}, []string{"quest"}, nil
}

// TestScreenImportRowsVocabularyError pins AC-8 of GH#4492: when a retained
// record needs the custom status/type vocabulary, the screen either reads it
// or stops — it never guesses. A resolver error aborts with the row's line and
// id (contextualizing the read error) WITHOUT rejecting the row, a store
// with no vocabulary capability cannot screen such a record at all, and the
// read happens at most once, only for the record that needs it.
func TestScreenImportRowsVocabularyError(t *testing.T) {
	now := time.Now().UTC()
	customRow := &types.Issue{
		ID:        "imve-aaa",
		Title:     "Custom status",
		Status:    types.Status("verify"),
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	sources := map[*types.Issue]importRecordSource{
		customRow: {Line: 3, Raw: `{"id":"imve-aaa"}`},
	}

	t.Run("resolver error reports line and id without rejecting the row", func(t *testing.T) {
		vocab := &countingVocabulary{err: errors.New("dolt resolver unavailable")}
		kept, rejects, err := screenImportRows(context.Background(), vocab, []*types.Issue{customRow}, sources)
		if err == nil {
			t.Fatal("a vocabulary read error must stop the screen; got nil")
		}
		for _, want := range []string{"line 3", "imve-aaa", "dolt resolver unavailable"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error must carry %q; got:\n%s", want, err)
			}
		}
		if len(kept) != 0 || len(rejects) != 0 {
			t.Errorf("a vocabulary error rejects nothing and keeps nothing for writing (kept %d, rejects %d)", len(kept), len(rejects))
		}
		if vocab.reads != 1 {
			t.Errorf("the screen must read the vocabulary exactly once even when the read errors; read %d time(s)", vocab.reads)
		}
	})

	t.Run("missing capability is an error when a row needs the vocabulary", func(t *testing.T) {
		kept, rejects, err := screenImportRows(context.Background(), nil, []*types.Issue{customRow}, sources)
		if err == nil {
			t.Fatalf("a store with no vocabulary capability must not screen a custom-value record (it cannot); kept %d, rejects %d", len(kept), len(rejects))
		}
		for _, want := range []string{"line 3", "imve-aaa", "vocabulary"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error must carry %q; got:\n%s", want, err)
			}
		}
		if len(rejects) != 0 {
			t.Errorf("a missing capability must not reject the row that needs the vocabulary; got %d reject(s)", len(rejects))
		}
	})

	t.Run("built-in rows never read the vocabulary", func(t *testing.T) {
		builtinRow := &types.Issue{
			ID:        "imve-bbb",
			Title:     "Built-in status",
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		}
		vocab := &countingVocabulary{err: errors.New("must not be read")}
		kept, rejects, err := screenImportRows(context.Background(), vocab, []*types.Issue{builtinRow}, nil)
		if err != nil {
			t.Fatalf("a file of built-in records must screen without the vocabulary: %v", err)
		}
		if vocab.reads != 0 {
			t.Errorf("the vocabulary read is lazy; a built-in-only file must not read it (read %d time(s))", vocab.reads)
		}
		if len(kept) != 1 || len(rejects) != 0 {
			t.Errorf("the built-in row must be kept; kept %d, rejects %d", len(kept), len(rejects))
		}
	})
}
