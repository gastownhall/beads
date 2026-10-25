package httpclient

import "github.com/steveyegge/beads/internal/storage"

// BlockedStateMaintainedByServer satisfies storage.ServerMaintainedBlockedState:
// the server derives and maintains is_blocked (issueops.BlockedStateInvariant's
// local-write clause runs on every write the server commits) and the column
// lives in the server's database, so storage.RecomputeBlocked answers
// storage.BlockedMaintainedByServer for this store and dials nothing.
func (s *Store) BlockedStateMaintainedByServer() bool { return true }

var _ storage.ServerMaintainedBlockedState = (*Store)(nil)
