package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

func runConfigSetProxiedServer(ctx context.Context, key, value string) error {
	if key == "status.custom" && value != "" {
		if _, err := types.ParseCustomStatusConfig(value); err != nil {
			return HandleErrorRespectJSON("invalid status.custom value: %v", err)
		}
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, key, value); err != nil {
			return "", fmt.Errorf("setting config: %w", err)
		}
		return fmt.Sprintf("bd: config set %s", key), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("failed: %v", err)
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
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	value, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetConfig(ctx, key)
	})
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
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	cfg, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (map[string]string, error) {
		return uw.ConfigUseCase().GetAllConfig(ctx)
	})
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
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().DeleteConfig(ctx, key); err != nil {
			return "", fmt.Errorf("deleting config: %w", err)
		}
		return fmt.Sprintf("bd: config unset %s", key), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("failed: %v", err)
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

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	return uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		cfgUC := uw.ConfigUseCase()
		for i, k := range keys {
			if err := cfgUC.SetConfig(ctx, k, values[i]); err != nil {
				return "", fmt.Errorf("setting config %s: %w", k, err)
			}
		}
		return fmt.Sprintf("bd: config set-many (%d keys)", len(keys)), nil
	})
}
