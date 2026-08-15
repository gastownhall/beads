package workapi

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The shared, DATABASE-FREE half of issueops.Sweeper: what a sweep request
// means, which candidates survive the recheck, and what counts as a citation.
// Every implementation of the role runs these, so `bd purge` and `bd prune`
// have one definition rather than one per backend.
//
// What is NOT here is the sweep itself. Selecting rows and deleting them needs
// one transaction (issueops.Sweeper.Sweep), which no interface above a store
// publishes; the bodies live in internal/storage/issueops/sweep.go and in the
// unit-of-work provider.

// ValidateSweepRequest applies the request rules every Sweeper implementation
// shares, before anything is read.
//
// The require-a-filter refusal for the durable tier is a safety invariant, so
// it lives HERE rather than in a CLI handler: a second front door inherits it
// by calling the role. See issueops.SweepRequest.
func ValidateSweepRequest(in issueops.SweepRequest) error {
	switch in.Tier {
	case issueops.SweepEphemeral, issueops.SweepDurable:
	case "":
		return fmt.Errorf("%w: sweep requires a tier (%q or %q), and has no default",
			issueops.ErrValidation, issueops.SweepEphemeral, issueops.SweepDurable)
	default:
		return fmt.Errorf("%w: %q is not a sweep tier; use %q or %q",
			issueops.ErrValidation, in.Tier, issueops.SweepEphemeral, issueops.SweepDurable)
	}
	if in.IDPattern != "" {
		// filepath.Match reports a malformed pattern on any subject, so one
		// probe against the empty string classifies the pattern itself. The
		// front doors used to discard this error, which turned `--pattern '['`
		// into "nothing matched" on a command whose job is to delete matches.
		if _, err := filepath.Match(in.IDPattern, ""); err != nil {
			return fmt.Errorf("%w: --pattern %q is not a valid glob: %v",
				issueops.ErrValidation, in.IDPattern, err)
		}
	}
	if in.Tier == issueops.SweepDurable && in.ClosedBefore == nil && in.IDPattern == "" {
		return fmt.Errorf("%w: a durable sweep requires a closed-before cutoff or an id pattern; "+
			"pass the pattern \"*\" to sweep every closed issue deliberately",
			issueops.ErrValidation)
	}
	return nil
}

// BuildSweepCandidateFilter turns a sweep request into the storage-level
// filter that selects its CANDIDATES: the closed rows of one tier, bounded by
// the cutoff.
//
// The pattern is deliberately NOT in the filter. Globs are matched in Go
// (MatchesSweepPattern) because a LIKE translation would silently disagree with
// filepath.Match on `[...]` and on the escape rules — a disagreement that, on
// this operation, decides which rows are deleted.
//
// Call ValidateSweepRequest first; this builder assumes a validated request
// and does not re-refuse one.
func BuildSweepCandidateFilter(in issueops.SweepRequest) types.IssueFilter {
	closed := types.StatusClosed
	ephemeral := in.Tier == issueops.SweepEphemeral
	filter := types.IssueFilter{
		Status:    &closed,
		Ephemeral: &ephemeral,
	}
	if in.ClosedBefore != nil {
		cutoff := *in.ClosedBefore
		filter.ClosedBefore = &cutoff
	}
	return filter
}

// MatchesSweepPattern reports whether an id is admitted by a request's
// IDPattern. An empty pattern admits everything.
//
// A malformed pattern reports false here; ValidateSweepRequest is what refuses it.
func MatchesSweepPattern(pattern, id string) bool {
	if pattern == "" {
		return true
	}
	ok, err := filepath.Match(pattern, id)
	return err == nil && ok
}

// FilterSweepCandidates applies the pattern and then the two protections that
// hold a candidate back before any reference scan: the pinned flag, and the
// closed_at recheck.
//
// THE ORDER IS PART OF THE ANSWER, and issueops.Sweeper.Sweep states it: the
// pattern narrows first, so a pinned row the pattern excluded is not counted
// as protected — it was never a candidate. The three closed_at buckets are
// defenses against the tier query returning a row it was not asked for, counted
// rather than dropped so a disagreement between the query and the recheck is
// visible instead of silent.
//
// cutoff is the request's ClosedBefore; a nil cutoff performs the presence
// checks and skips the comparison.
func FilterSweepCandidates(issues []*types.Issue, pattern string, cutoff *time.Time) ([]*types.Issue, issueops.SweepSkips) {
	kept := make([]*types.Issue, 0, len(issues))
	var skips issueops.SweepSkips

	for _, issue := range issues {
		if issue == nil {
			skips.Unreadable++
			continue
		}
		if !MatchesSweepPattern(pattern, issue.ID) {
			continue
		}
		switch {
		case issue.Pinned:
			skips.Pinned++
		case issue.Status != types.StatusClosed:
			skips.NotClosed++
		case issue.ClosedAt == nil:
			skips.UnknownClosedAt++
		case cutoff != nil && !issue.ClosedAt.Before(*cutoff):
			skips.ClosedAtOrAfterCutoff++
		default:
			kept = append(kept, issue)
		}
	}
	return kept, skips
}

// SweepDefenseSkips is how many candidates the recheck rejected as
// self-inconsistent — the three closed_at buckets plus the unreadable row. It
// is separate from the two PROTECTIONS (pinned, referenced) because a non-zero
// value here is a defense firing rather than a normal outcome, and the front
// doors warn on exactly that.
func SweepDefenseSkips(s issueops.SweepSkips) int {
	return s.Unreadable + s.NotClosed + s.UnknownClosedAt + s.ClosedAtOrAfterCutoff
}

// NotDoneStatusesForSweep is the status vocabulary a reference scan asks:
// every built-in status that is not "closed", plus every configured custom
// status whose category is not "done".
//
// It is an explicit LIST rather than a not-closed exclusion because that is
// what the storage seam takes reliably on both planes (a Statuses set, not an
// ExcludeStatus). Reading the workspace's custom statuses is required, not
// best-effort: a scan that missed a custom active status would under-protect
// and delete a bead a live bead still cites.
func NotDoneStatusesForSweep(custom []types.CustomStatus) []types.Status {
	statuses := []types.Status{
		types.StatusOpen,
		types.StatusInProgress,
		types.StatusBlocked,
		types.StatusDeferred,
		types.StatusPinned,
		types.StatusHooked,
	}
	for _, cs := range custom {
		if cs.Category != types.CategoryDone {
			statuses = append(statuses, types.Status(cs.Name))
		}
	}
	return statuses
}

// BuildSweepReferenceScanFilter selects the rows a reference scan reads: the
// not-done set, and nothing else about them.
func BuildSweepReferenceScanFilter(custom []types.CustomStatus) types.IssueFilter {
	return types.IssueFilter{Statuses: NotDoneStatusesForSweep(custom)}
}

// CandidateIDMatcher finds literal, word-bounded occurrences of a candidate id
// set in arbitrary text. It is the whole of what "cited" means for
// issueops.SweepRequest.ProtectReferenced.
//
// It is bucketed by FIRST BYTE and each bucket sorted longest-first: the scan
// runs over every not-done bead's description, notes and comments, where a
// naive strings.Contains per candidate is O(candidates x text). Longest-first
// inside a bucket is what makes `be-1.1` win over `be-1` where both are
// candidates and both start at the same offset.
type CandidateIDMatcher struct {
	byFirstByte map[byte][]string
}

// NewCandidateIDMatcher builds a matcher over a candidate id set. Empty ids
// are dropped: they would match at every boundary.
func NewCandidateIDMatcher(candidateIDs map[string]bool) CandidateIDMatcher {
	byFirstByte := make(map[byte][]string)
	for id := range candidateIDs {
		if id == "" {
			continue
		}
		byFirstByte[id[0]] = append(byFirstByte[id[0]], id)
	}
	for first := range byFirstByte {
		ids := byFirstByte[first]
		sort.Slice(ids, func(i, j int) bool {
			if len(ids[i]) == len(ids[j]) {
				return ids[i] < ids[j]
			}
			return len(ids[i]) > len(ids[j])
		})
		byFirstByte[first] = ids
	}
	return CandidateIDMatcher{byFirstByte: byFirstByte}
}

// FindAll adds every candidate id occurring in text at ASCII word boundaries
// to found.
func (m CandidateIDMatcher) FindAll(text string, found map[string]bool) {
	for i := 0; i < len(text); i++ {
		ids := m.byFirstByte[text[i]]
		if len(ids) == 0 || !isWordBoundaryAt(text, i) {
			continue
		}
		for _, id := range ids {
			end := i + len(id)
			if end <= len(text) && strings.HasPrefix(text[i:], id) && isWordBoundaryAt(text, end) {
				found[id] = true
				break
			}
		}
	}
}

func isWordBoundaryAt(s string, idx int) bool {
	var before, after byte
	if idx > 0 {
		before = s[idx-1]
	}
	if idx < len(s) {
		after = s[idx]
	}
	return isASCIIWordByte(before) != isASCIIWordByte(after)
}

func isASCIIWordByte(b byte) bool {
	return b == '_' ||
		('0' <= b && b <= '9') ||
		('A' <= b && b <= 'Z') ||
		('a' <= b && b <= 'z')
}

// PartitionSweepReferenced splits candidates into the ones no not-done row
// cites and the ones some row does, and returns the bounded sample
// issueops.SweepResult.ReferencedIDs carries. Both the kept slice and the
// sample preserve the candidate order, so two runs over one snapshot report the
// same ids.
func PartitionSweepReferenced(candidates []*types.Issue, referenced map[string]bool) (kept []*types.Issue, referencedCount int, sample []string) {
	kept = make([]*types.Issue, 0, len(candidates))
	for _, issue := range candidates {
		if referenced[issue.ID] {
			referencedCount++
			if len(sample) < issueops.SweepReferencedSampleLimit {
				sample = append(sample, issue.ID)
			}
			continue
		}
		kept = append(kept, issue)
	}
	return kept, referencedCount, sample
}
