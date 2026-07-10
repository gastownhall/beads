package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

func openConfigProxiedUOW(ctx context.Context) (uow.UnitOfWork, error) {
	if uowProvider == nil {
		return nil, HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return nil, HandleErrorRespectJSON("open unit of work: %v", err)
	}
	return uw, nil
}

func runConfigSetProxiedServer(ctx context.Context, key, value string) error {
	if key == "status.custom" && value != "" {
		if _, err := types.ParseCustomStatusConfig(value); err != nil {
			return HandleErrorRespectJSON("invalid status.custom value: %v", err)
		}
	}

	uw, err := openConfigProxiedUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if err := uw.ConfigUseCase().SetConfig(ctx, key, value); err != nil {
		return HandleErrorRespectJSON("Error setting config: %v", err)
	}

	if err := uow.CommitWithRetries(ctx, uw, fmt.Sprintf("bd: config set %s", key)); err != nil && !isDoltNothingToCommit(err) {
		return HandleErrorRespectJSON("failed to commit: %v", err)
	}

	if jsonOutput {
		_ = outputJSON(map[string]string{
			"key":   key,
			"value": value,
		})
	} else {
		fmt.Printf("Set %s = %s\n", key, value)
	}
	printConfigSideEffects(checkConfigSetSideEffects(key, value))
	return nil
}

func runConfigGetProxiedServer(ctx context.Context, key string) error {
	uw, err := openConfigProxiedUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	value, err := uw.ConfigUseCase().GetConfig(ctx, key)
	if err != nil {
		return HandleErrorRespectJSON("Error getting config: %v", err)
	}

	if jsonOutput {
		_ = outputJSON(map[string]string{
			"key":   key,
			"value": value,
		})
		return nil
	}
	if value == "" {
		fmt.Printf("%s (not set)\n", key)
	} else {
		fmt.Printf("%s\n", value)
	}
	return nil
}

func runConfigListProxiedServer(ctx context.Context) error {
	uw, err := openConfigProxiedUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	cfg, err := uw.ConfigUseCase().GetAllConfig(ctx)
	if err != nil {
		return HandleErrorRespectJSON("Error listing config: %v", err)
	}

	if jsonOutput {
		_ = outputJSON(cfg)
		return nil
	}

	if len(cfg) == 0 {
		fmt.Println("No configuration set")
		return nil
	}

	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("\nConfiguration:")
	for _, k := range keys {
		fmt.Printf("  %s = %s\n", k, cfg[k])
	}

	showConfigYAMLOverrides(cfg)
	return nil
}

func runConfigUnsetProxiedServer(ctx context.Context, key string) error {
	uw, err := openConfigProxiedUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if err := uw.ConfigUseCase().DeleteConfig(ctx, key); err != nil {
		return HandleErrorRespectJSON("Error deleting config: %v", err)
	}

	if err := uow.CommitWithRetries(ctx, uw, fmt.Sprintf("bd: config unset %s", key)); err != nil && !isDoltNothingToCommit(err) {
		return HandleErrorRespectJSON("failed to commit: %v", err)
	}

	if jsonOutput {
		_ = outputJSON(map[string]string{
			"key": key,
		})
	} else {
		fmt.Printf("Unset %s\n", key)
	}
	printConfigSideEffects(checkConfigUnsetSideEffects(key))
	return nil
}

func runConfigSetManyProxiedServer(ctx context.Context, keys, values []string) error {
	if len(keys) == 0 {
		return nil
	}
	uw, err := openConfigProxiedUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	cfgUC := uw.ConfigUseCase()
	for i, k := range keys {
		if err := cfgUC.SetConfig(ctx, k, values[i]); err != nil {
			return HandleErrorRespectJSON("Error setting config %s: %v", k, err)
		}
	}

	if err := uow.CommitWithRetries(ctx, uw, fmt.Sprintf("bd: config set-many (%d keys)", len(keys))); err != nil && !isDoltNothingToCommit(err) {
		return HandleErrorRespectJSON("failed to commit: %v", err)
	}
	return nil
}
