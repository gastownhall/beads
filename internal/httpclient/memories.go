// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/memories.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// httpMemories serves memoryops.Memories from the four memory operations
// (design D8 row 15).
//
// The role is one plane and one interface, so the accessor cannot be flipped by
// halves: the two writes this bead owns arrive with the two reads that share
// their vocabulary, and all four go through the same key rules.
//
// THE MISS IS A RESULT, NOT AN ERROR. memoryops deliberately declares no
// ErrNotFound: a recall or a forget that found nothing answers Found false with
// a nil error. The wire spells the same miss as a 404, which the problem mapper
// turns into issueops.ErrNotFound — so the one piece of real translation in this
// file is turning that back into the role's own answer. Getting it wrong would
// make `bd recall` of an unknown key an error where every other backend prints
// "not found".
type httpMemories struct {
	store *Store
	wire  WriteWire
}

var _ memoryops.Memories = (*httpMemories)(nil)

// Remember stores one memory. An ABSENT key asks the server to derive one from
// the content, which is why the wire member is a pointer: an empty string is not
// the same request as an omitted member, and the derived key comes back in the
// result so the caller learns where the memory landed.
func (m *httpMemories) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	if strings.TrimSpace(req.Content) == "" {
		return memoryops.RememberResult{}, invalid("content is required")
	}

	body := apigen.RememberRequest{Content: req.Content}
	if req.Key != "" {
		key := req.Key
		body.Key = &key
	}
	res, err := m.wire.RememberMemory(ctx, body)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	return memoryops.RememberResult{Key: res.Key, Value: res.Value, Replaced: res.Replaced}, nil
}

// Recall reads one memory.
func (m *httpMemories) Recall(ctx context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	if err := requireMemoryKey(req.Key); err != nil {
		return memoryops.RecallResult{}, err
	}
	res, err := m.wire.RecallMemory(ctx, req.Key)
	if errors.Is(err, issueops.ErrNotFound) {
		return memoryops.RecallResult{Key: req.Key}, nil
	}
	if err != nil {
		return memoryops.RecallResult{}, err
	}
	// Found is Value != "": the role conflates a stored empty string with an
	// absent memory, and so does the wire, which 404s the same row.
	return memoryops.RecallResult{Key: req.Key, Value: res.Value, Found: res.Value != ""}, nil
}

// Forget removes one memory and reports the value it removed.
func (m *httpMemories) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	if err := requireMemoryKey(req.Key); err != nil {
		return memoryops.ForgetResult{}, err
	}
	res, err := m.wire.ForgetMemory(ctx, req.Key)
	if errors.Is(err, issueops.ErrNotFound) {
		// Nothing was stored under the key, and nothing was deleted. A result,
		// not an error: forgetting what was never remembered is the state the
		// caller asked for.
		return memoryops.ForgetResult{Key: req.Key}, nil
	}
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	return memoryops.ForgetResult{Key: req.Key, Value: res.Value, Found: true}, nil
}

// List enumerates the memory plane, narrowed by Search.
//
// The folding is the ROLE's, so it happens server-side: the raw term goes on the
// wire as `search` and the server's own role lowercases both sides. Folding it
// here as well would be a second implementation of a rule that has one.
func (m *httpMemories) List(ctx context.Context, req memoryops.ListRequest) (memoryops.ListResult, error) {
	page, err := m.wire.ListMemories(ctx, req.Search)
	if err != nil {
		return memoryops.ListResult{}, err
	}
	// Empty map, never nil: a caller ranges over it without a nil check, and
	// "the plane is empty" is not something to spell as an absent answer.
	out := make(map[string]string, len(page.Items))
	for _, memory := range page.Items {
		out[memory.Key] = memory.Value
	}
	return memoryops.ListResult{Memories: out}, nil
}

// requireMemoryKey applies the role's trim rule before a key becomes a path.
//
// The server refuses a blank key on these two operations as well, but as a 400
// alongside the 404 that means "no such memory" — and a caller cannot act on the
// difference if the client never made it. Refusing here keeps the role's
// ErrValidation promise and keeps a request that cannot succeed off the wire.
func requireMemoryKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return invalid("memory key is required")
	}
	return nil
}
