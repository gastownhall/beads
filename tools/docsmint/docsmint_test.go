package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedStaging(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "build", "cli-docs", "index.md"), `---
title: CLI Reference
description: Generated reference for every bd command
---

<!-- AUTO-GENERATED: do not edit manually -->

Generated from `+"`bd help --docs-root`"+`.

## Commands

- [`+"`bd create`"+`](./create.md)
- [`+"`bd show`"+`](./show.md)
`)
	writeFile(t, filepath.Join(root, "build", "cli-docs", "show.md"), `---
title: "bd show"
description: "Show an issue"
---

<!-- AUTO-GENERATED: do not edit manually -->

Generated from `+"`bd help --doc show`"+`.

Show an issue

`+"```"+`
bd show <id>
`+"```"+`
`)
	writeFile(t, filepath.Join(root, "build", "cli-docs", "create.md"), `---
title: "bd create"
---

<!-- AUTO-GENERATED: do not edit manually -->

Create an issue
`)
	writeFile(t, filepath.Join(root, "docs", "CLI_REFERENCE.md"), `# bd CLI reference

Generated single-file reference.
`)
	writeFile(t, filepath.Join(root, "docs", "docs.json"), `{
  "name": "Beads Documentation",
  "navigation": {
    "groups": [
      {
        "group": "Getting Started",
        "pages": [
          "index"
        ]
      },
      {
        "group": "Reference",
        "pages": [
          "reference/index",
          {
            "group": "CLI Reference",
            "pages": [
              "cli-reference/index",
              "cli-reference/stale-command"
            ]
          }
        ]
      }
    ]
  }
}
`)
}

func TestRunTransformsPagesToMintlifyForm(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)

	if err := run(root); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	show, err := os.ReadFile(filepath.Join(root, "docs", "cli-reference", "show.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(show)
	if !strings.Contains(got, "{/* AUTO-GENERATED: do not edit manually */}") {
		t.Errorf("show.md missing JSX comment marker:\n%s", got)
	}
	if strings.Contains(got, "<!--") {
		t.Errorf("show.md still contains an HTML comment (invalid in Mintlify MDX):\n%s", got)
	}
	if !strings.Contains(got, "bd show <id>") {
		t.Errorf("show.md lost its usage fence:\n%s", got)
	}

	index, err := os.ReadFile(filepath.Join(root, "docs", "cli-reference", "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "](/cli-reference/show)") {
		t.Errorf("index.md links not rewritten to extensionless routes:\n%s", index)
	}
	if strings.Contains(string(index), "](./") {
		t.Errorf("index.md still contains relative .md links:\n%s", index)
	}
}

func TestRunSplicesNavAndPreservesOtherGroups(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)

	if err := run(root); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "docs", "docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, want := range []string{
		`"cli-reference/index"`,
		`"cli-reference/create"`,
		`"cli-reference/show"`,
		`"Getting Started"`,
		`"reference/index"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("docs.json missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "stale-command") {
		t.Errorf("docs.json still lists a stale CLI page:\n%s", got)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("docs.json is no longer valid JSON: %v\n%s", err, got)
	}

	// Idempotent: a second run must not change anything.
	if err := run(root); err != nil {
		t.Fatalf("run() second pass error = %v", err)
	}
	again, err := os.ReadFile(filepath.Join(root, "docs", "docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Errorf("nav splice is not idempotent:\nfirst:\n%s\nsecond:\n%s", data, again)
	}
}

// Mintlify's prebuild re-serializes markdown and dedents indented prose, so a
// line whose first token is `export` or `import` — legal CommonMark in bd's
// generic output — lands at column 0 and gets parsed as an MDX ESM block,
// failing the whole page (reproduced on /cli-reference/mail). docsmint must
// neutralize such lines outside code fences.
func TestRunNeutralizesMDXESMHazardLines(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	writeFile(t, filepath.Join(root, "build", "cli-docs", "mail.md"), `---
title: "bd mail"
---

Examples:
  # Configure delegation (one-time setup)
  export BEADS_MAIL_DELEGATE="gt mail"
  # or
  import something-that-looks-like-esm

`+"```"+`
  export FENCED_STAYS_VERBATIM=1
`+"```"+`
`)

	if err := run(root); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "docs", "cli-reference", "mail.md"))
	if err != nil {
		t.Fatal(err)
	}

	inFence := false
	for i, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !inFence && (strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "import ")) {
			t.Errorf("line %d is a bare ESM-hazard outside a fence: %q", i+1, line)
		}
	}
	if !strings.Contains(string(data), `export BEADS_MAIL_DELEGATE="gt mail"`) {
		t.Errorf("hazard line content was lost, not neutralized:\n%s", data)
	}
	if !strings.Contains(string(data), "  export FENCED_STAYS_VERBATIM=1") {
		t.Errorf("fenced export line must stay verbatim:\n%s", data)
	}
}

// HTML comments and relative .md links inside code fences are literal example
// content (e.g. a future cmd.Example demonstrating markdown) — the Mintlify
// rewrites must not touch them.
func TestRunLeavesFencedCommentAndLinkExamplesVerbatim(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	writeFile(t, filepath.Join(root, "build", "cli-docs", "fency.md"), `---
title: "bd fency"
---

<!-- AUTO-GENERATED: do not edit manually -->

See [the index](./index.md).

`+"```"+`
<!-- FENCED COMMENT STAYS -->
[fenced link](./fenced.md)
`+"```"+`
`)

	if err := run(root); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "docs", "cli-reference", "fency.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "<!-- FENCED COMMENT STAYS -->") {
		t.Errorf("fenced HTML comment was rewritten; fenced examples must stay verbatim:\n%s", got)
	}
	if !strings.Contains(got, "[fenced link](./fenced.md)") {
		t.Errorf("fenced relative link was rewritten; fenced examples must stay verbatim:\n%s", got)
	}
	if !strings.Contains(got, "{/* AUTO-GENERATED: do not edit manually */}") {
		t.Errorf("out-of-fence HTML comment was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "](/cli-reference/index)") {
		t.Errorf("out-of-fence relative link was not rewritten:\n%s", got)
	}
}

// docs/CLI_REFERENCE.md is served by Mintlify as a hidden page but is emitted
// by bd without docsmint's per-page transforms, so it carries the same MDX ESM
// hazard (bare indented `export ...` from bd mail). docsmint must neutralize it.
func TestRunNeutralizesSingleFileReferenceESMHazards(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	writeFile(t, filepath.Join(root, "docs", "CLI_REFERENCE.md"), `# bd CLI reference

Examples:
  export BEADS_MAIL_DELEGATE="gt mail"

`+"```"+`
  export FENCED_STAYS_VERBATIM=1
`+"```"+`
`)

	if err := run(root); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "docs", "CLI_REFERENCE.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "`export BEADS_MAIL_DELEGATE=\"gt mail\"`") {
		t.Errorf("bare export line was not neutralized in CLI_REFERENCE.md:\n%s", got)
	}
	if !strings.Contains(got, "  export FENCED_STAYS_VERBATIM=1") {
		t.Errorf("fenced export line must stay verbatim:\n%s", got)
	}
}

func TestRunFailsLoudlyWithoutSingleFileReference(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	if err := os.Remove(filepath.Join(root, "docs", "CLI_REFERENCE.md")); err != nil {
		t.Fatal(err)
	}

	err := run(root)
	if err == nil {
		t.Fatal("run() succeeded without docs/CLI_REFERENCE.md; want loud failure")
	}
	if !strings.Contains(err.Error(), "CLI_REFERENCE.md") {
		t.Errorf("error should name the missing file: %v", err)
	}
}

// If docs.json is ever reformatted so the CLI Reference group's own "pages"
// key precedes its "group" key, a textual forward search finds the NEXT
// group's pages array and silently rewrites the wrong group. The splice must
// fail closed instead of corrupting navigation.
func TestRunFailsClosedWhenPagesIsNotSiblingKey(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	reordered := `{
  "navigation": {
    "groups": [
      {
        "pages": [
          "cli-reference/index"
        ],
        "group": "CLI Reference"
      },
      {
        "group": "Other",
        "pages": [
          "other/index"
        ]
      }
    ]
  }
}
`
	writeFile(t, filepath.Join(root, "docs", "docs.json"), reordered)

	err := run(root)
	if err == nil {
		t.Fatal("run() succeeded with a reordered CLI Reference group; want fail-closed error")
	}

	data, readErr := os.ReadFile(filepath.Join(root, "docs", "docs.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != reordered {
		t.Errorf("docs.json was modified despite the fail-closed error:\n%s", data)
	}
}

func TestRunRemovesStaleTargetPages(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	stale := filepath.Join(root, "docs", "cli-reference", "removed-command.md")
	writeFile(t, stale, "stale\n")

	if err := run(root); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale target page survived the run")
	}
}

func TestRunFailsLoudlyWithoutStaging(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "docs.json"), `{"navigation":{"groups":[{"group":"CLI Reference","pages":[]}]}}`)

	err := run(root)
	if err == nil {
		t.Fatal("run() succeeded with no staging tree; want loud failure")
	}
	if !strings.Contains(err.Error(), "build/cli-docs") {
		t.Errorf("error should name the missing staging dir: %v", err)
	}
}

func TestRunFailsLoudlyWithoutCLIReferenceGroup(t *testing.T) {
	root := t.TempDir()
	seedStaging(t, root)
	writeFile(t, filepath.Join(root, "docs", "docs.json"), `{"navigation":{"groups":[{"group":"Other","pages":[]}]}}`)

	err := run(root)
	if err == nil {
		t.Fatal("run() succeeded without a CLI Reference nav group; want loud failure")
	}
	if !strings.Contains(err.Error(), "CLI Reference") {
		t.Errorf("error should name the missing group: %v", err)
	}
}
