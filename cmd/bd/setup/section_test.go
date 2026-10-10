package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSectionWrapsBody(t *testing.T) {
	section := ManagedSection("body line\n")
	if !strings.HasPrefix(section, agentsBeginMarker+"\n") {
		t.Fatalf("section does not start with the begin marker: %q", section)
	}
	if !strings.Contains(section, "\n"+agentsEndMarker+"\n") {
		t.Fatalf("section does not end with the end marker: %q", section)
	}
	if strings.Contains(section, "body line\n\n") {
		t.Fatalf("body was not right-trimmed: %q", section)
	}
}

func TestUpsertManagedSectionAppendsToUserContent(t *testing.T) {
	got, replaced, err := UpsertManagedSection("# Mine\n\nkeep me\n", "beads body")
	if err != nil {
		t.Fatalf("UpsertManagedSection: %v", err)
	}
	if replaced {
		t.Fatalf("reported a replacement where there was no section")
	}
	if !strings.HasPrefix(got, "# Mine\n\nkeep me\n") {
		t.Fatalf("user content was not preserved verbatim: %q", got)
	}
	if !ContainsManagedSection(got) {
		t.Fatalf("section was not added: %q", got)
	}
	if !strings.Contains(got, "beads body") {
		t.Fatalf("body missing from result: %q", got)
	}
}

func TestUpsertManagedSectionReplacesExistingSection(t *testing.T) {
	first, _, err := UpsertManagedSection("# Mine\n\nkeep me\n", "old body")
	if err != nil {
		t.Fatalf("first UpsertManagedSection: %v", err)
	}
	got, replaced, err := UpsertManagedSection(first, "new body")
	if err != nil {
		t.Fatalf("UpsertManagedSection: %v", err)
	}
	if !replaced {
		t.Fatalf("did not report replacing the existing section")
	}
	if strings.Contains(got, "old body") {
		t.Fatalf("old body survived: %q", got)
	}
	if !strings.Contains(got, "new body") {
		t.Fatalf("new body missing: %q", got)
	}
	if !strings.Contains(got, "# Mine\n\nkeep me\n") {
		t.Fatalf("user content was lost: %q", got)
	}
	if n := strings.Count(got, agentsBeginMarker); n != 1 {
		t.Fatalf("expected exactly one begin marker, got %d in %q", n, got)
	}
}

func TestUpsertManagedSectionEmptyFileGetsSectionOnly(t *testing.T) {
	got, replaced, err := UpsertManagedSection("   \n", "body")
	if err != nil {
		t.Fatalf("UpsertManagedSection: %v", err)
	}
	if replaced {
		t.Fatalf("reported a replacement on an empty file")
	}
	if got != ManagedSection("body") {
		t.Fatalf("empty file did not become exactly the section: %q", got)
	}
}

func TestUpsertManagedSectionUnbalancedMarkersRefuse(t *testing.T) {
	broken := "# My rules\n\nSee " + agentsBeginMarker + " for the old note.\n\nKEEP-ME-1 user paragraph.\n"
	if _, _, err := UpsertManagedSection(broken, "body"); !errors.Is(err, ErrUnbalancedSection) {
		t.Fatalf("expected ErrUnbalancedSection for a stray BEGIN, got %v", err)
	}

	// The other way round: an END with no BEGIN.
	endOnly := "# Mine\n\n" + agentsEndMarker + "\n"
	if _, _, err := UpsertManagedSection(endOnly, "body"); !errors.Is(err, ErrUnbalancedSection) {
		t.Fatalf("expected ErrUnbalancedSection for a stray END, got %v", err)
	}
}

// A file whose content a previous version of setup wrote as the whole file is
// beads' own output, so the section replaces it rather than being appended to
// it - otherwise the upgrade path duplicates the guidance and --remove leaves
// the unmarked copy behind for good.
func TestUpsertManagedSectionMigratesLegacyTemplate(t *testing.T) {
	const template = "# GitHub Copilot Instructions\n\n## Core Workflow\n"
	got, replaced, err := UpsertManagedSection(template+"\n", template)
	if err != nil {
		t.Fatalf("UpsertManagedSection: %v", err)
	}
	if !replaced {
		t.Fatalf("legacy template should be reported as replaced in place")
	}
	if n := strings.Count(got, "# GitHub Copilot Instructions"); n != 1 {
		t.Fatalf("expected the guidance once, got %d copies in %q", n, got)
	}
	if got != ManagedSection(template) {
		t.Fatalf("legacy file did not become exactly the section: %q", got)
	}

	// Trailing whitespace must not decide the question.
	if _, replaced, err := UpsertManagedSection(template, template); err != nil || !replaced {
		t.Fatalf("exact legacy content not migrated: replaced=%v err=%v", replaced, err)
	}

	// After the migration --remove takes the whole file away, because nothing
	// of the user's is left in it.
	remaining, hasUser, err := RemoveManagedSection(got)
	if err != nil {
		t.Fatalf("RemoveManagedSection: %v", err)
	}
	if hasUser || strings.TrimSpace(remaining) != "" {
		t.Fatalf("a migrated file reported user content: %q", remaining)
	}

	// Content that differs from the template is the user's and is preserved.
	userFile := template + "\n## House rules\n\nAlways run make check.\n"
	withUser, replaced, err := UpsertManagedSection(userFile, template)
	if err != nil {
		t.Fatalf("UpsertManagedSection: %v", err)
	}
	if replaced {
		t.Fatalf("edited user file was treated as a legacy template")
	}
	if !strings.Contains(withUser, "Always run make check.") {
		t.Fatalf("user content was lost: %q", withUser)
	}
}

// An unbalanced marker must not let a later run or --remove span from the stray
// BEGIN to a subsequent END and delete the user's text in between.
func TestUnbalancedMarkersDoNotEatUserTextOnReRunOrRemove(t *testing.T) {
	broken := "# My rules\n\nSee " + agentsBeginMarker + " for the old note.\n\nKEEP-ME-1 user paragraph.\n"

	if _, err := InstallManagedSectionFileIn(t, broken, "body"); err == nil {
		t.Fatalf("install accepted a file with a stray BEGIN marker")
	}
	after, err := RemoveManagedSectionFileIn(t, broken)
	if err == nil {
		t.Fatalf("remove accepted a file with a stray BEGIN marker")
	}
	if after != broken {
		t.Fatalf("unbalanced file was rewritten: %q", after)
	}
}

// install/remove against a real file, returning the file content afterwards.
func InstallManagedSectionFileIn(t *testing.T, seed, body string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "copilot-instructions.md")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := InstallManagedSectionFile(path, body); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(data), nil
}

func RemoveManagedSectionFileIn(t *testing.T, seed string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "copilot-instructions.md")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	remaining, hasUser, err := RemoveManagedSection(string(data))
	if err != nil {
		return string(data), err
	}
	if !hasUser {
		return "", nil
	}
	if err := WriteManagedSectionFile(path, remaining); err != nil {
		return string(data), err
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(after), nil
}

func TestRemoveManagedSectionReportsLeftoverUserContent(t *testing.T) {
	withUser, _, err := UpsertManagedSection("# Mine\n\nkeep me\n", "body")
	if err != nil {
		t.Fatalf("UpsertManagedSection: %v", err)
	}
	remaining, hasUser, err := RemoveManagedSection(withUser)
	if err != nil {
		t.Fatalf("RemoveManagedSection: %v", err)
	}
	if !hasUser {
		t.Fatalf("user content should have been reported as remaining in %q", remaining)
	}
	if !strings.Contains(remaining, "keep me") {
		t.Fatalf("user content was dropped: %q", remaining)
	}
	if strings.Contains(remaining, agentsBeginMarker) || strings.Contains(remaining, agentsEndMarker) {
		t.Fatalf("markers survived removal: %q", remaining)
	}

	onlySection := ManagedSection("body")
	remaining, hasUser, err = RemoveManagedSection(onlySection)
	if err != nil {
		t.Fatalf("RemoveManagedSection: %v", err)
	}
	if hasUser {
		t.Fatalf("a beads-only file reported user content: %q", remaining)
	}
	if strings.TrimSpace(remaining) != "" {
		t.Fatalf("a beads-only file left content behind: %q", remaining)
	}
}

func TestInstallManagedSectionFilePreservesExistingInstructions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "copilot-instructions.md")
	if err := os.WriteFile(path, []byte("# Project conventions\n\nAlways run make check.\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	action, err := InstallManagedSectionFile(path, "beads body")
	if err != nil {
		t.Fatalf("InstallManagedSectionFile: %v", err)
	}
	if action != SectionAdded {
		t.Fatalf("expected SectionAdded over an existing user file, got %q", action)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "Always run make check.") {
		t.Fatalf("user instructions were lost: %q", got)
	}
	if !ContainsManagedSection(got) || !strings.Contains(got, "beads body") {
		t.Fatalf("beads section missing after install: %q", got)
	}

	// A second run replaces the section in place and is still idempotent.
	action, err = InstallManagedSectionFile(path, "beads body")
	if err != nil {
		t.Fatalf("second InstallManagedSectionFile: %v", err)
	}
	if action != SectionUpdated {
		t.Fatalf("expected SectionUpdated on re-run, got %q", action)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back again: %v", err)
	}
	if string(again) != got {
		t.Fatalf("re-install was not idempotent:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

func TestInstallManagedSectionFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.md")
	link := filepath.Join(dir, "copilot-instructions.md")
	if err := os.WriteFile(target, []byte("user\n"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := InstallManagedSectionFile(link, "beads body"); err == nil {
		t.Fatalf("expected a symlink write to be refused")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "user\n" {
		t.Fatalf("symlink target was modified: %q", data)
	}
}
