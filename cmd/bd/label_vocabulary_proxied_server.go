package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/ui"
)

// runLabelDefineProxiedServer is `bd label define`'s proxied-server route,
// reached through the same LabelVocabularyUseCase the direct route's
// store.DefineLabel takes -- both call issueops.DefineLabelInTx underneath
// (see internal/storage/domain/db/label_vocabulary.go).
func runLabelDefineProxiedServer(ctx context.Context, label, description string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.LabelVocabularyUseCase().Define(ctx, label, description, actor); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: label define %s", strings.TrimSpace(label)), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	commandDidWrite.Store(true)

	trimmed := strings.TrimSpace(label)
	if jsonOutput {
		return outputJSON(map[string]interface{}{"status": "defined", "label": trimmed})
	}
	fmt.Printf("%s Defined label %q\n", ui.RenderPass(glyphCheck), trimmed)
	return nil
}

// runLabelUndefineProxiedServer is `bd label undefine`'s proxied-server
// route.
func runLabelUndefineProxiedServer(ctx context.Context, label string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.LabelVocabularyUseCase().Undefine(ctx, label); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: label undefine %s", strings.TrimSpace(label)), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	commandDidWrite.Store(true)

	trimmed := strings.TrimSpace(label)
	if jsonOutput {
		return outputJSON(map[string]interface{}{"status": "undefined", "label": trimmed})
	}
	fmt.Printf("%s Undefined label %q\n", ui.RenderPass(glyphCheck), trimmed)
	return nil
}

// runLabelDefinedProxiedServer is `bd label defined`'s proxied-server route.
// The in-use counts come from countAllLabelsProxied, the same fold
// `bd label list-all` (proxied) uses.
func runLabelDefinedProxiedServer(ctx context.Context) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	defs, err := uw.LabelVocabularyUseCase().List(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	counts, err := countAllLabelsProxied(ctx, uw)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	rows := make([]labelDefinedInfo, 0, len(defs))
	for _, d := range defs {
		row := labelDefinedInfo{
			Label:      d.Label,
			CreatedAt:  d.CreatedAt.UTC().Format("2006-01-02"),
			InUseCount: counts[d.Label],
		}
		if d.Description != nil {
			row.Description = *d.Description
		}
		if d.CreatedBy != nil {
			row.CreatedBy = *d.CreatedBy
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })

	return renderLabelDefinedRows(rows)
}
