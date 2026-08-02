package main

import (
	"context"
)

func runTypesProxiedServer(ctx context.Context) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	var customTypes []string
	if ct, err := uw.ConfigUseCase().GetCustomTypes(ctx); err == nil {
		customTypes = ct
	}

	return renderTypes(customTypes)
}
