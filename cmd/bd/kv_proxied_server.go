package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/uow"
)

// Proxied-server handlers for `bd kv`. The kv store is a thin prefix layer
// (kv.*) over the config table, so these mirror config_proxied_server.go:
// one RunTx per write invocation with a real commit message, RunTxRead for
// reads. Key validation happens in the RunE before dispatch; presentation
// goes through the shared printKV* helpers so output cannot drift from the
// classic path (downstream mail transports parse `kv list --json`).
//
// Error handling note: the underlying storage error is passed through %v
// unmodified — callers retry on the 'Merge conflict detected' /
// 'constraint violation, transaction rolled back' substrings, which must
// survive to the CLI surface.

func runKVSetProxiedServer(ctx context.Context, key, value string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	storageKey := kvPrefix + key
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, storageKey, value); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: kv set %s", key), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("setting key: %v", err)
	}
	commandDidWrite.Store(true)

	return printKVSetResult(key, value)
}

func runKVGetProxiedServer(ctx context.Context, key string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	storageKey := kvPrefix + key
	value, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetConfig(ctx, storageKey)
	})
	if err != nil {
		return HandleErrorRespectJSON("getting key: %v", err)
	}

	return printKVGetResult(key, value)
}

func runKVClearProxiedServer(ctx context.Context, key string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	storageKey := kvPrefix + key
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().DeleteConfig(ctx, storageKey); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: kv clear %s", key), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("deleting key: %v", err)
	}
	commandDidWrite.Store(true)

	return printKVClearResult(key)
}

func runKVListProxiedServer(ctx context.Context) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	allConfig, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (map[string]string, error) {
		return uw.ConfigUseCase().GetAllConfig(ctx)
	})
	if err != nil {
		return HandleErrorRespectJSON("listing keys: %v", err)
	}

	return printKVListResult(kvPairsFromConfig(allConfig))
}
