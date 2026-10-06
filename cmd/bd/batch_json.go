package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// JSON input projects directly onto the existing guarded BatchApplier role.
// It adds a CLI transport, not another interpretation of batch semantics.
type batchJSONPlan struct {
	SchemaVersion string                     `json:"schema_version"`
	Request       issueops.ApplyBatchRequest `json:"request"`
}

func parseBatchJSON(reader io.Reader, actorName string) (issueops.ApplyBatchRequest, error) {
	var plan batchJSONPlan
	const maxBytes = 8 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return plan.Request, err
	}
	if len(raw) > maxBytes {
		return plan.Request, fmt.Errorf("JSON batch exceeds %d bytes", maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan.Request, err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return plan.Request, fmt.Errorf("JSON batch requires one object")
	}
	if plan.SchemaVersion != "1" {
		return plan.Request, fmt.Errorf("unsupported JSON batch schema")
	}
	if plan.Request.Actor != "" {
		return plan.Request, fmt.Errorf("batch actor comes from the CLI actor, not the JSON request")
	}
	plan.Request.Actor = actorName
	if _, err := storage.PlanApplyBatch(plan.Request); err != nil {
		return plan.Request, err
	}
	return plan.Request, nil
}

func runBatchJSON(ctx context.Context, request issueops.ApplyBatchRequest, proxied bool) (issueops.ApplyBatchResult, error) {
	var applier issueops.BatchApplier
	var err error
	if proxied {
		applier, err = uow.NewBatchApplier(uowProvider)
	} else {
		source, ok := store.(interface {
			BatchApplier() (issueops.BatchApplier, error)
		})
		if !ok {
			return issueops.ApplyBatchResult{}, fmt.Errorf("provider does not support guarded JSON batches")
		}
		applier, err = source.BatchApplier()
	}
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	return applier.ApplyBatch(ctx, request)
}
