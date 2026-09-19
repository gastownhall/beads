package workapi

import (
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
)

// ValidateMetadataFilters refuses a metadata key the query layer cannot spell.
//
// Both builders call it, so the leaf contract's "keys are validated inside" is
// true of every Reader implementation rather than of the CLI only. Before it
// existed the sole check lived in the SQL builder, whose error surfaced wrapped
// in the storage method's name — a shape nothing above storage can classify —
// so a typo'd key reached `bd list` as a usage error and the HTTP surface as a
// 500, on a parameter the frozen document promises a 400 for.
//
// The CLI still validates the same input at flag-parse time and still reports
// it in its own words: this is the floor under every caller, not a replacement
// for a front door's usage error.
//
// Keys are checked in sorted order so a request with two bad keys always names
// the same one.
func ValidateMetadataFilters(fields map[string]string, hasKey string) error {
	if hasKey != "" {
		if err := storage.ValidateMetadataKey(hasKey); err != nil {
			return fmt.Errorf("invalid metadata key filter: %w", err)
		}
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := storage.ValidateMetadataKey(k); err != nil {
			return fmt.Errorf("invalid metadata field key: %w", err)
		}
	}
	return nil
}
