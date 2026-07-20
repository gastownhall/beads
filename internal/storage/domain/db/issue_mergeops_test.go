package db

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestIssueSQLRepository_MergeOps covers the read-merge-write operation keys
// (metadata edits, note appends) on the domain/db repository — the write path
// behind the proxied-server CLI. The ops must be resolved against the row read
// inside the SAME unit-of-work transaction, not against a snapshot the caller
// read earlier: pre-merged values from a stale MVCC snapshot are exactly the
// lost-update defect (concurrent writers erased each other's committed keys
// while both exited 0).
func (s *testSuite) TestIssueSQLRepository_MergeOps() {
	s.Run("SetMetadataMergesIntoCurrentRow", s.issueUpdateSetMetadataMerges)
	s.Run("MergeMetadataOverlaysTopLevelKeys", s.issueUpdateMergeMetadataOverlays)
	s.Run("UnsetMetadataRemovesKeys", s.issueUpdateUnsetMetadataRemoves)
	s.Run("AppendNotesConcatenates", s.issueUpdateAppendNotes)
	s.Run("AppendNotesToEmptyHasNoLeadingNewline", s.issueUpdateAppendNotesEmpty)
	s.Run("AppendNotesRejectsCombinedReplacement", s.issueUpdateAppendNotesConflict)
	s.Run("SetMetadataRejectsCombinedReplacement", s.issueUpdateSetMetadataConflict)
	s.Run("OpsRouteToWispsTable", s.issueUpdateMergeOpsWispRouting)
}

func (s *testSuite) seedIssueWithMetadata(id, metadata, notes string) {
	r := s.issueRepo()
	in := newTestIssue(id, "merge ops seed")
	if metadata != "" {
		in.Metadata = json.RawMessage(metadata)
	}
	in.Notes = notes
	s.Require().NoError(r.Insert(s.Ctx(), in, "seeder", domain.InsertIssueOpts{}))
}

func (s *testSuite) readMetadataMap(id string, opts domain.IssueTableOpts) map[string]any {
	out, err := s.issueRepo().Get(s.Ctx(), id, opts)
	s.Require().NoError(err)
	got := map[string]any{}
	if len(out.Metadata) > 0 {
		s.Require().NoError(json.Unmarshal(out.Metadata, &got))
	}
	return got
}

func (s *testSuite) issueUpdateSetMetadataMerges() {
	s.seedIssueWithMetadata("bd-mo-set", `{"existing":"yes"}`, "")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-set", map[string]any{
		issueops.OpSetMetadata: []string{"tier=gold", "score=99"},
	}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	got := s.readMetadataMap("bd-mo-set", domain.IssueTableOpts{})
	s.Equal("yes", got["existing"], "pre-existing key must survive a set-metadata edit")
	s.Equal("gold", got["tier"])
	s.Equal("99", got["score"], "--set-metadata values are stored as JSON strings (GH#4146)")
}

func (s *testSuite) issueUpdateMergeMetadataOverlays() {
	s.seedIssueWithMetadata("bd-mo-merge", `{"a":1,"b":2}`, "")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-merge", map[string]any{
		issueops.OpMergeMetadata: json.RawMessage(`{"b":3,"c":4}`),
	}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	got := s.readMetadataMap("bd-mo-merge", domain.IssueTableOpts{})
	s.Equal(float64(1), got["a"])
	s.Equal(float64(3), got["b"])
	s.Equal(float64(4), got["c"])
	s.Len(got, 3)
}

func (s *testSuite) issueUpdateUnsetMetadataRemoves() {
	s.seedIssueWithMetadata("bd-mo-unset", `{"keep":"yes","drop":"yes"}`, "")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-unset", map[string]any{
		issueops.OpUnsetMetadata: []string{"drop"},
	}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	got := s.readMetadataMap("bd-mo-unset", domain.IssueTableOpts{})
	s.Equal("yes", got["keep"])
	s.NotContains(got, "drop")
}

func (s *testSuite) issueUpdateAppendNotes() {
	s.seedIssueWithMetadata("bd-mo-notes", "", "first")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-notes", map[string]any{
		issueops.OpAppendNotes: "second",
	}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	out, err := r.Get(s.Ctx(), "bd-mo-notes", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("first\nsecond", out.Notes)
}

func (s *testSuite) issueUpdateAppendNotesEmpty() {
	s.seedIssueWithMetadata("bd-mo-notes-empty", "", "")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-notes-empty", map[string]any{
		issueops.OpAppendNotes: "only",
	}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	out, err := r.Get(s.Ctx(), "bd-mo-notes-empty", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("only", out.Notes)
}

func (s *testSuite) issueUpdateAppendNotesConflict() {
	s.seedIssueWithMetadata("bd-mo-notes-conf", "", "first")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-notes-conf", map[string]any{
		issueops.OpAppendNotes: "second",
		"notes":                "replacement",
	}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), "notes replacement")
}

func (s *testSuite) issueUpdateSetMetadataConflict() {
	s.seedIssueWithMetadata("bd-mo-set-conf", `{"a":1}`, "")
	r := s.issueRepo()

	err := r.Update(s.Ctx(), "bd-mo-set-conf", map[string]any{
		issueops.OpSetMetadata: []string{"b=2"},
		"metadata":             json.RawMessage(`{"c":3}`),
	}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), "metadata replacement")
}

func (s *testSuite) issueUpdateMergeOpsWispRouting() {
	r := s.issueRepo()
	in := newTestIssue("bd-mo-wisp", "wisp merge ops")
	in.Metadata = json.RawMessage(`{"kind":"wisp"}`)
	in.Notes = "w1"
	s.Require().NoError(r.Insert(s.Ctx(), in, "seeder", domain.InsertIssueOpts{UseWispsTable: true}))

	err := r.Update(s.Ctx(), "bd-mo-wisp", map[string]any{
		issueops.OpSetMetadata: []string{"extra=1"},
		issueops.OpAppendNotes: "w2",
	}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)

	out, err := r.Get(s.Ctx(), "bd-mo-wisp", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("w1\nw2", out.Notes)
	got := map[string]any{}
	s.Require().NoError(json.Unmarshal(out.Metadata, &got))
	s.Equal("wisp", got["kind"], "wisp's pre-existing metadata key must survive")
	s.Equal("1", got["extra"])
}

// TestIssueUseCase_ApplyUpdateMergeOps proves the ops flow through the domain
// use-case layer — the exact call chain the proxied-server update handler uses
// (issueUC.ApplyUpdate with spec.Fields).
func (s *testSuite) TestIssueUseCase_ApplyUpdateMergeOps() {
	s.Run("SetMetadataViaApplyUpdatePreservesSiblingKeys", s.iucApplyUpdateSetMetadata)
	s.Run("AppendNotesViaApplyUpdate", s.iucApplyUpdateAppendNotes)
}

func (s *testSuite) iucApplyUpdateSetMetadata() {
	s.seedIssueWithMetadata("bd-mo-uc", `{"sibling":"safe"}`, "")

	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-mo-uc", domain.UpdateSpec{
		Fields: map[string]any{
			issueops.OpSetMetadata: []string{"mine=1"},
		},
	}, "tester")
	s.Require().NoError(err)

	got := map[string]any{}
	s.Require().NoError(json.Unmarshal(updated.Metadata, &got))
	s.Equal("safe", got["sibling"])
	s.Equal("1", got["mine"])
}

func (s *testSuite) iucApplyUpdateAppendNotes() {
	s.seedIssueWithMetadata("bd-mo-uc-notes", "", "base")

	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-mo-uc-notes", domain.UpdateSpec{
		Fields: map[string]any{
			issueops.OpAppendNotes: "appended",
		},
	}, "tester")
	s.Require().NoError(err)
	s.Equal("base\nappended", updated.Notes)
}
