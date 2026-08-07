package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/uow"
)

// The last proxied-server handler for the memory surface: `bd remember`. The
// other three verbs now reach memoryops.Memories through openMemories, like
// every other converged command, and this one follows in W5 — at which point
// there is nothing left in this file. Memories are a thin prefix layer
// (kv.memory.*) over the config table, so these mirror
// config_proxied_server.go / kv_proxied_server.go: one RunTx per write
// invocation with a real commit message, RunTxRead for reads. Validation and
// key derivation happen in the RunE before dispatch; presentation goes
// through the shared helpers in memory.go so output cannot drift from the
// classic path. Storage keys stay kv.memory.<key> so export tooling picks
// memories up alongside the rest of the kv namespace.
//
// Error handling note: the underlying storage error is passed through %v
// unmodified — callers retry on the 'Merge conflict detected' /
// 'constraint violation, transaction rolled back' substrings, which must
// survive to the CLI surface.

// memoryGetProxied reads one kv.memory.* value; mirrors the classic path's
// tolerant read (`existing, _ := store.GetConfig(...)`) where noted.
func memoryGetProxied(ctx context.Context, storageKey string) (string, error) {
	return uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetConfig(ctx, storageKey)
	})
}

func runRememberProxiedServer(ctx context.Context, key, insight string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	storageKey := kvPrefix + memoryPrefix + key

	// Classic path ignores the existence-check error; mirror that.
	existing, _ := memoryGetProxied(ctx, storageKey)
	verb := "Remembered"
	if existing != "" {
		verb = "Updated"
	}

	// Desire path + footgun guard (see the classic RunE in memory.go): a bare
	// slug with no --key is a mistyped READ — recall it if it exists, refuse
	// to store it as its own content if it doesn't. No write happens on this
	// branch, so it runs before the RunTx.
	if memoryKeyFlag == "" && slugify(insight) == insight {
		return rememberBareKeyPath(key, insight, existing)
	}

	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, storageKey, insight); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: remember %s", key), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("storing memory: %v", err)
	}

	return printRememberResult(verb, key, insight)
}
