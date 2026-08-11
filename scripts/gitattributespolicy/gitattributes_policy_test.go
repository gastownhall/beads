package gitattributespolicy_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryTextPolicyKeepsLFWithAutoCrlf(t *testing.T) {
	eolTestRuns.Add(1)
	t.Cleanup(func() {
		if t.Skipped() {
			eolTestSkipped.Add(1)
			return
		}
		if !t.Failed() {
			eolTestPasses.Add(1)
		}
	})

	repository := t.TempDir()
	gitPath := resolveEOLPolicyGit(t)
	requireEOLPolicyGitVersion(t, gitPath, repository)

	attributes, err := os.ReadFile(filepath.Join("..", "..", ".gitattributes"))
	if err != nil {
		t.Fatalf("read repository attributes: %v", err)
	}

	writeEOLPolicyFixture(t, filepath.Join(repository, ".gitattributes"), attributes)
	textFixtures := []struct {
		name     string
		contents []byte
	}{
		{"fixture.go", []byte("package fixture\n\nconst value = 1\n")},
		{"future.unlisted", []byte("future text format\n")},
	}
	for _, fixture := range textFixtures {
		writeEOLPolicyFixture(
			t,
			filepath.Join(repository, fixture.name),
			fixture.contents)
	}
	binaryPath := filepath.Join(repository, "fixture.jpg")
	binaryBytes := []byte{0xff, 0xd8, 0x00, 0x0d, 0x0a, 0xff, 0xd9}
	writeEOLPolicyFixture(t, binaryPath, binaryBytes)

	runEOLPolicyGit(t, gitPath, repository, "init", "--quiet")
	runEOLPolicyGit(t, gitPath, repository, "config", "--local", "user.name", "EOL Policy Test")
	runEOLPolicyGit(t, gitPath, repository, "config", "--local", "user.email", "eol-policy@example.invalid")
	runEOLPolicyGit(t, gitPath, repository, "config", "--local", "core.hooksPath", ".git/hooks")
	runEOLPolicyGit(t, gitPath, repository, "config", "--local", "core.autocrlf", "true")
	if hooksPath := strings.TrimSpace(runEOLPolicyGit(
		t,
		gitPath,
		repository,
		"config",
		"--local",
		"--get",
		"core.hooksPath")); hooksPath != ".git/hooks" {
		t.Fatalf("repo-local core.hooksPath = %q, want .git/hooks", hooksPath)
	}
	runEOLPolicyGit(t, gitPath, repository, "add", "--", ".")
	runEOLPolicyGit(t, gitPath, repository, "commit", "--quiet", "-m", "fixture")

	for _, fixture := range textFixtures {
		if err := os.Remove(filepath.Join(repository, fixture.name)); err != nil {
			t.Fatalf("remove text fixture %s before checkout: %v", fixture.name, err)
		}
	}
	if err := os.Remove(binaryPath); err != nil {
		t.Fatalf("remove binary fixture before checkout: %v", err)
	}
	runEOLPolicyGit(t, gitPath, repository, "checkout-index", "--force", "--all")

	for _, fixture := range textFixtures {
		checkedOutText, err := os.ReadFile(filepath.Join(repository, fixture.name))
		if err != nil {
			t.Fatalf("read checked-out text fixture %s: %v", fixture.name, err)
		}
		if !bytes.Equal(fixture.contents, checkedOutText) {
			t.Fatalf(
				"text checkout changed LF bytes for %s:\nwant %q\ngot  %q",
				fixture.name,
				fixture.contents,
				checkedOutText)
		}
	}

	checkedOutBinary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read checked-out binary fixture: %v", err)
	}
	if !bytes.Equal(binaryBytes, checkedOutBinary) {
		t.Fatalf("binary checkout changed bytes:\nwant %v\ngot  %v", binaryBytes, checkedOutBinary)
	}

	attributesOutput := runEOLPolicyGit(
		t,
		gitPath,
		repository,
		"check-attr",
		"-z",
		"text",
		"diff",
		"merge",
		"eol",
		"--",
		"fixture.go",
		"future.unlisted",
		"fixture.jpg")
	attributeRecords, err := parseGitAttributeRecords(attributesOutput)
	if err != nil {
		t.Fatalf("parse exact Git attributes: %v", err)
	}
	if err := compareExactGitAttributeRecords(
		attributeRecords,
		expectedGitAttributeRecords()); err != nil {
		t.Fatalf("Git attributes differ from the exact requested set: %v\n%q", err, attributesOutput)
	}

	if status := strings.TrimSpace(
		runEOLPolicyGit(t, gitPath, repository, "status", "--porcelain")); status != "" {
		t.Fatalf("checkout is dirty under core.autocrlf=true:\n%s", status)
	}
}

func TestGitAttributeRecordsRejectLookalikeValues(t *testing.T) {
	records, err := parseGitAttributeRecords(
		"fixture.jpg\x00diff\x00unsetfoo\x00")
	if err != nil {
		t.Fatalf("parse lookalike fixture: %v", err)
	}
	expected := map[gitAttributeKey]string{
		{path: "fixture.jpg", attribute: "diff"}: "unset",
	}
	if err := compareExactGitAttributeRecords(records, expected); err == nil {
		t.Fatal("lookalike diff value satisfied the exact unset record")
	}
}

func TestGitAttributeRecordsRejectMalformedInput(t *testing.T) {
	const record = "fixture.jpg\x00diff\x00unset\x00"
	tests := []struct {
		name       string
		output     string
		wantReason string
	}{
		{name: "unterminated", output: "fixture.jpg\x00diff\x00unset", wantReason: "final NUL"},
		{name: "truncated", output: "fixture.jpg\x00diff\x00", wantReason: "triples"},
		{name: "empty path", output: "\x00diff\x00unset\x00", wantReason: "empty record field"},
		{name: "empty attribute", output: "fixture.jpg\x00\x00unset\x00", wantReason: "empty record field"},
		{name: "empty value", output: "fixture.jpg\x00diff\x00\x00", wantReason: "empty record field"},
		{name: "identical duplicate", output: record + record, wantReason: "duplicate path/attribute"},
		{
			name: "conflicting duplicate",
			output: record +
				"fixture.jpg\x00diff\x00unsetfoo\x00",
			wantReason: "duplicate path/attribute",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGitAttributeRecords(test.output)
			if err == nil {
				t.Fatal("malformed git check-attr records were accepted")
			}
			if !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("failure = %q, want reason %q", err, test.wantReason)
			}
		})
	}
}

func TestGitAttributeRecordsRequireCompleteExpectedSet(t *testing.T) {
	expected := expectedGitAttributeRecords()
	missing := cloneGitAttributeRecords(expected)
	delete(missing, gitAttributeKey{path: "fixture.go", attribute: "diff"})
	extra := cloneGitAttributeRecords(expected)
	extra[gitAttributeKey{path: "fixture.extra", attribute: "diff"}] = "unset"
	for _, test := range []struct {
		name   string
		actual map[gitAttributeKey]string
	}{
		{name: "missing unasserted record", actual: missing},
		{name: "extra record", actual: extra},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := compareExactGitAttributeRecords(test.actual, expected); err == nil {
				t.Fatal("non-exact git check-attr record set was accepted")
			}
		})
	}
}

type gitAttributeKey struct {
	path      string
	attribute string
}

func parseGitAttributeRecords(output string) (map[gitAttributeKey]string, error) {
	fields := strings.Split(output, "\x00")
	if fields[len(fields)-1] != "" {
		return nil, fmt.Errorf(
			"git check-attr -z output lacks its final NUL: %q",
			output)
	}
	fields = fields[:len(fields)-1]
	if len(fields)%3 != 0 {
		return nil, fmt.Errorf(
			"git check-attr -z output is not path/attribute/value triples: %q",
			output)
	}
	records := make(map[gitAttributeKey]string, len(fields)/3)
	for index := 0; index < len(fields); index += 3 {
		key := gitAttributeKey{path: fields[index], attribute: fields[index+1]}
		value := fields[index+2]
		if key.path == "" || key.attribute == "" || value == "" {
			return nil, fmt.Errorf(
				"git check-attr -z returned an empty record field: key=%+v value=%q",
				key,
				value)
		}
		if _, duplicate := records[key]; duplicate {
			return nil, fmt.Errorf(
				"git check-attr -z returned a duplicate path/attribute: %+v",
				key)
		}
		records[key] = value
	}
	return records, nil
}

func expectedGitAttributeRecords() map[gitAttributeKey]string {
	return map[gitAttributeKey]string{
		{path: "fixture.go", attribute: "text"}:       "auto",
		{path: "fixture.go", attribute: "diff"}:       "unspecified",
		{path: "fixture.go", attribute: "merge"}:      "unspecified",
		{path: "fixture.go", attribute: "eol"}:        "lf",
		{path: "future.unlisted", attribute: "text"}:  "auto",
		{path: "future.unlisted", attribute: "diff"}:  "unspecified",
		{path: "future.unlisted", attribute: "merge"}: "unspecified",
		{path: "future.unlisted", attribute: "eol"}:   "lf",
		{path: "fixture.jpg", attribute: "text"}:      "unset",
		{path: "fixture.jpg", attribute: "diff"}:      "unset",
		{path: "fixture.jpg", attribute: "merge"}:     "unset",
		{path: "fixture.jpg", attribute: "eol"}:       "lf",
	}
}

func compareExactGitAttributeRecords(
	actual map[gitAttributeKey]string,
	expected map[gitAttributeKey]string,
) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("record count = %d, want %d", len(actual), len(expected))
	}
	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			return fmt.Errorf("missing record for %+v", key)
		}
		if got != want {
			return fmt.Errorf("record %+v value = %q, want %q", key, got, want)
		}
	}
	return nil
}

func cloneGitAttributeRecords(
	source map[gitAttributeKey]string,
) map[gitAttributeKey]string {
	clone := make(map[gitAttributeKey]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func writeEOLPolicyFixture(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
