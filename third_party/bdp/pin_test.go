// Package bdp holds the byte-identity test for the vendored gastownhall/bdp
// files under spec/ and their pin and lock, bdp.pin. Both are written only by
// scripts/bdp-vendor.sh; engdocs/design/bdp.md has the rules for moving the
// pin.
package bdp

import (
	"bufio"
	"bytes"
	"crypto/sha1" // #nosec G505 -- git's blob id, an identity, not a security hash.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const hint = "regenerate with scripts/bdp-vendor.sh <bdp checkout> <tag>, or undo an accidental edit with " +
	"`git restore --source=origin/main --staged --worktree -- third_party/bdp` and delete any untracked file " +
	"under third_party/bdp/spec/; never edit third_party/bdp by hand (engdocs/design/bdp.md)"

// pinRepository and pinSpecRevisionRule are the exact values
// scripts/bdp-vendor.sh writes; keep them in step with REPOSITORY and
// SPEC_REVISION_RULE there.
const (
	pinRepository       = "https://github.com/gastownhall/bdp"
	pinSpecRevisionRule = "git log -1 --format=%H <commit> -- specs/profiles/core/core.md specs/profiles/write/write.md " +
		"specs/profiles/types/types.md specs/profiles/events/events.md (bdp scripts/canonical-spec-revision.mjs; not --first-parent)"
)

// pinKeys are the key/value lines bdp.pin carries, each exactly once and in
// this order. The last, lock, is lockOf the pin's other non-comment lines.
var pinKeys = []string{"repository", "tag", "tag-object", "commit", "spec-revision", "spec-revision-rule", "lock"}

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type pinFile struct {
	path   string
	size   int64
	sha256 string
	blob   string
}

type pin struct {
	values map[string]string
	files  []pinFile
}

// parsePin reads bdp.pin: '#' comment lines, then "key value" lines in
// pinKeys order, then one "file <sha256> <size> <git-blob-sha1> <path>" line
// per vendored file, sorted by path. Every non-comment line must be exactly
// what scripts/bdp-vendor.sh writes, so a reordered, re-spaced or otherwise
// re-formatted line fails even when it parses, and the lock line must be
// lockOf the others, so a pin merged from two script runs fails too.
func parsePin(data []byte) (*pin, []error) {
	p := &pin{values: map[string]string{}}
	var errs []error
	known := map[string]bool{}
	for _, k := range pinKeys {
		known[k] = true
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
		key, value, ok := strings.Cut(line, " ")
		if !ok || value == "" {
			errs = append(errs, fmt.Errorf("bdp.pin:%d: %q is not a \"key value\" line", n, line))
			continue
		}
		if key == "file" {
			f := strings.Fields(value)
			if len(f) != 4 {
				errs = append(errs, fmt.Errorf("bdp.pin:%d: want \"file <sha256> <size> <git-blob> <path>\", got %q", n, line))
				continue
			}
			size, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil || size < 0 || !hex64.MatchString(f[0]) || !hex40.MatchString(f[2]) {
				errs = append(errs, fmt.Errorf("bdp.pin:%d: malformed file line %q", n, line))
				continue
			}
			p.files = append(p.files, pinFile{path: f[3], size: size, sha256: f[0], blob: f[2]})
			continue
		}
		if !known[key] {
			errs = append(errs, fmt.Errorf("bdp.pin:%d: unknown key %q", n, key))
			continue
		}
		if strings.TrimSpace(value) != value || strings.Contains(value, "  ") || strings.ContainsAny(value, "\t\r") {
			errs = append(errs, fmt.Errorf("bdp.pin:%d: %s line is not in the script's exact format (stray whitespace): %q", n, key, line))
			continue
		}
		if _, dup := p.values[key]; dup {
			errs = append(errs, fmt.Errorf("bdp.pin:%d: duplicate key %q", n, key))
			continue
		}
		p.values[key] = value
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Errorf("reading bdp.pin: %w", err))
	}
	for _, k := range pinKeys {
		if _, ok := p.values[k]; !ok {
			errs = append(errs, fmt.Errorf("bdp.pin: missing key %q", k))
		}
	}
	for _, k := range []string{"tag-object", "commit", "spec-revision"} {
		if v, ok := p.values[k]; ok && !hex40.MatchString(v) {
			errs = append(errs, fmt.Errorf("bdp.pin: %s %q is not a 40-hex object id", k, v))
		}
	}
	if v, ok := p.values["lock"]; ok && !hex64.MatchString(v) {
		errs = append(errs, fmt.Errorf("bdp.pin: lock %q is not a 64-hex sha256", v))
	}
	if v, ok := p.values["tag"]; ok && !strings.HasPrefix(v, "spec/") {
		errs = append(errs, fmt.Errorf("bdp.pin: tag %q is not a bdp checkpoint tag (spec/*)", v))
	}
	if v, ok := p.values["repository"]; ok && v != pinRepository {
		errs = append(errs, fmt.Errorf("bdp.pin: repository %q, want %q", v, pinRepository))
	}
	if v, ok := p.values["spec-revision-rule"]; ok && v != pinSpecRevisionRule {
		errs = append(errs, fmt.Errorf("bdp.pin: spec-revision-rule %q, want the script's rule %q", v, pinSpecRevisionRule))
	}

	// Canonical form: the lines the script would write for these values.
	var want []string
	for _, k := range pinKeys {
		if v, ok := p.values[k]; ok {
			want = append(want, k+" "+v)
		}
	}
	files := append([]pinFile(nil), p.files...)
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for i, f := range files {
		if i > 0 && f.path == files[i-1].path {
			errs = append(errs, fmt.Errorf("bdp.pin: %q is listed twice", f.path))
		}
		want = append(want, fmt.Sprintf("file %s %d %s %s", f.sha256, f.size, f.blob, f.path))
	}
	if len(errs) == 0 && strings.Join(lines, "\n") != strings.Join(want, "\n") {
		for i := 0; i < len(lines) || i < len(want); i++ {
			var got, exp string
			if i < len(lines) {
				got = lines[i]
			}
			if i < len(want) {
				exp = want[i]
			}
			if got != exp {
				errs = append(errs, fmt.Errorf("bdp.pin is not in the script's exact format: non-comment line %d is %q, want %q", i+1, got, exp))
				break
			}
		}
	}
	// Checked after the format check, which runs only on an error-free pin:
	// nearly every edit of a non-comment line also leaves the lock stale, and
	// that error must not hide the format error.
	if v, ok := p.values["lock"]; ok && hex64.MatchString(v) {
		if got := lockOf(lines); got != v {
			errs = append(errs, fmt.Errorf("bdp.pin: lock %s does not match its other non-comment lines (sha256 %s): "+
				"bdp.pin was edited by hand or merged from two script runs; take main's bdp.pin and rerun the script", v, got))
		}
	}
	return p, errs
}

// lockOf is what bdp.pin's lock line holds: the sha256 of every other
// non-comment line, in file order, each ending in a newline (as
// `grep -v -e '^#' -e '^lock ' bdp.pin | sha256sum` computes it). Two script
// runs that differ in any line write different lock lines, so two PRs that
// each change bdp.pin conflict on that line instead of merging cleanly into
// a pin that mixes their files. Keep it in step with scripts/bdp-vendor.sh.
func lockOf(lines []string) string {
	h := sha256.New()
	for _, line := range lines {
		if key, _, _ := strings.Cut(line, " "); key == "lock" {
			continue
		}
		h.Write([]byte(line + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// gitBlobID is git's object id for a blob: sha1("blob <size>\x00" + bytes).
func gitBlobID(data []byte) string {
	h := sha1.New() // #nosec G401 -- git's blob id, an identity, not a security hash.
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// checkVendored checks dir (a third_party/bdp tree): bdp.pin parses, its
// non-comment lines are in the script's exact format and its lock line
// matches them, the files under spec/ are exactly its file lines, each
// matches its size, sha256 and git blob id, and LICENSE is among them. It
// cannot check that tag, tag-object, commit or spec-revision name the real
// bdp checkpoint; that needs bdp's history (scripts/bdp-vendor.sh --check).
func checkVendored(dir string) []error {
	data, err := os.ReadFile(filepath.Join(dir, "bdp.pin"))
	if err != nil {
		return []error{fmt.Errorf("reading bdp.pin: %w", err)}
	}
	p, errs := parsePin(data)

	specDir := filepath.Join(dir, "spec")
	onDisk := map[string]bool{}
	// Under Bazel each file in the runfiles tree is a symlink to its source,
	// so every non-directory entry counts as a file and is read through.
	walkErr := filepath.WalkDir(specDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(specDir, path)
		if err != nil {
			return err
		}
		onDisk[filepath.ToSlash(rel)] = true
		return nil
	})
	if walkErr != nil {
		errs = append(errs, fmt.Errorf("walking spec/: %w", walkErr))
	}

	listed := map[string]bool{}
	for _, f := range p.files {
		listed[f.path] = true
		if !onDisk[f.path] {
			errs = append(errs, fmt.Errorf("spec/%s is in bdp.pin but missing", f.path))
			continue
		}
		raw, err := os.ReadFile(filepath.Join(specDir, filepath.FromSlash(f.path)))
		if err != nil {
			errs = append(errs, fmt.Errorf("reading spec/%s: %w", f.path, err))
			continue
		}
		if int64(len(raw)) != f.size {
			errs = append(errs, fmt.Errorf("spec/%s is %d bytes, bdp.pin says %d", f.path, len(raw), f.size))
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != f.sha256 {
			errs = append(errs, fmt.Errorf("spec/%s sha256 = %s, bdp.pin says %s", f.path, got, f.sha256))
		}
		if got := gitBlobID(raw); got != f.blob {
			errs = append(errs, fmt.Errorf("spec/%s git blob = %s, bdp.pin says %s", f.path, got, f.blob))
		}
	}
	var extra []string
	for path := range onDisk {
		if !listed[path] {
			extra = append(extra, path)
		}
	}
	sort.Strings(extra)
	for _, path := range extra {
		errs = append(errs, fmt.Errorf("spec/%s is not in bdp.pin", path))
	}
	if !listed["LICENSE"] {
		errs = append(errs, fmt.Errorf("bdp.pin lists no LICENSE: bdp's MIT notice must ship with the copy"))
	}
	return errs
}

// TestVendoredBDPMatchesPin fails when a file under spec/ is changed, added
// or removed, when bdp.pin is malformed, its non-comment lines are not in the
// script's exact format or its lock line does not match them, or when bdp's
// LICENSE is missing. Whether the pin names the real bdp checkpoint is
// checked by `scripts/bdp-vendor.sh <bdp> <tag> --check`.
func TestVendoredBDPMatchesPin(t *testing.T) {
	errs := checkVendored(".")
	for _, err := range errs {
		t.Error(err)
	}
	if len(errs) > 0 {
		t.Log(hint)
	}
}

// writeTree writes a third_party/bdp tree into a temp dir: files under spec/
// and a bdp.pin describing them in the script's format, lock line included.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	lines := []string{
		"repository " + pinRepository,
		"tag spec/test-20260101",
		"tag-object " + strings.Repeat("a", 40),
		"commit " + strings.Repeat("b", 40),
		"spec-revision " + strings.Repeat("c", 40),
		"spec-revision-rule " + pinSpecRevisionRule,
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var fileLines []string
	for _, p := range paths {
		raw := []byte(files[p])
		writeSpecFile(t, dir, p, raw)
		fileLines = append(fileLines, fileLine(p, raw))
	}
	lock := "lock " + lockOf(append(append([]string(nil), lines...), fileLines...))
	lines = append(append(lines, lock), fileLines...)
	pin := "# test pin\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "bdp.pin"), []byte(pin), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeSpecFile writes raw to spec/<path> in a third_party/bdp tree.
func writeSpecFile(t *testing.T, dir, path string, raw []byte) {
	t.Helper()
	full := filepath.Join(dir, "spec", filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fileLine is the bdp.pin line the script writes for a vendored file.
func fileLine(path string, raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("file %s %d %s %s", hex.EncodeToString(sum[:]), len(raw), gitBlobID(raw), path)
}

// pinLockLine returns the lock line of dir's bdp.pin.
func pinLockLine(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "bdp.pin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "lock ") {
			return line
		}
	}
	t.Fatal("bdp.pin fixture has no lock line")
	return ""
}

func editFile(t *testing.T, path string, edit func(string) string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(raw))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// editPin applies a string replacement to bdp.pin and fails the test if the
// replacement did not apply, so a case can't pass vacuously.
func editPin(t *testing.T, dir, old, new string) {
	t.Helper()
	editFile(t, filepath.Join(dir, "bdp.pin"), func(s string) string {
		if !strings.Contains(s, old) {
			t.Fatalf("bdp.pin fixture lacks %q", old)
		}
		return strings.Replace(s, old, new, 1)
	})
}

func TestCheckVendoredCatchesHandEdits(t *testing.T) {
	base := map[string]string{
		"LICENSE":           "MIT License\n", // 12 bytes
		"specs/a.json":      "{\"a\": 1}\n",
		"packages/b/c.json": "[]\n",
	}
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, dir string)
		want   string // substring of some error; "" means no errors
	}{
		{"clean", func(*testing.T, string) {}, ""},
		{"clean with extra comment lines", func(t *testing.T, dir string) {
			editPin(t, dir, "# test pin\n", "# test pin\n# another comment\n")
		}, ""},
		{"one-byte edit", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "spec", "specs", "a.json"), func(s string) string { return strings.Replace(s, "1", "2", 1) })
		}, "spec/specs/a.json sha256"},
		{"one byte appended", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "spec", "packages", "b", "c.json"), func(s string) string { return s + " " })
		}, "spec/packages/b/c.json is 4 bytes"},
		{"extra file", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "spec", "specs", "extra.md"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "spec/specs/extra.md is not in bdp.pin"},
		{"missing file", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "spec", "specs", "a.json")); err != nil {
				t.Fatal(err)
			}
		}, "spec/specs/a.json is in bdp.pin but missing"},
		{"deleted LICENSE", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "spec", "LICENSE")); err != nil {
				t.Fatal(err)
			}
		}, "spec/LICENSE is in bdp.pin but missing"},
		{"LICENSE dropped from pin and tree", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "spec", "LICENSE")); err != nil {
				t.Fatal(err)
			}
			editFile(t, filepath.Join(dir, "bdp.pin"), func(s string) string {
				var keep []string
				for _, l := range strings.Split(s, "\n") {
					if !strings.HasSuffix(l, " LICENSE") {
						keep = append(keep, l)
					}
				}
				return strings.Join(keep, "\n")
			})
		}, "lists no LICENSE"},
		{"short commit id", func(t *testing.T, dir string) {
			editPin(t, dir, "commit "+strings.Repeat("b", 40), "commit 286648546c")
		}, "commit \"286648546c\" is not a 40-hex"},
		{"pin key missing", func(t *testing.T, dir string) {
			editPin(t, dir, "spec-revision "+strings.Repeat("c", 40)+"\n", "")
		}, "missing key \"spec-revision\""},
		{"pin unknown key", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "bdp.pin"), func(s string) string { return s + "branch main\n" })
		}, "unknown key \"branch\""},
		{"key lines reordered", func(t *testing.T, dir string) {
			editPin(t, dir, "tag spec/test-20260101\ntag-object "+strings.Repeat("a", 40)+"\n",
				"tag-object "+strings.Repeat("a", 40)+"\ntag spec/test-20260101\n")
		}, "not in the script's exact format"},
		{"file lines reordered", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "bdp.pin"), func(s string) string {
				lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
				n := len(lines)
				lines[n-1], lines[n-2] = lines[n-2], lines[n-1]
				return strings.Join(lines, "\n") + "\n"
			})
		}, "not in the script's exact format"},
		{"extra whitespace in a file line", func(t *testing.T, dir string) {
			editPin(t, dir, " LICENSE\n", "  LICENSE\n")
		}, "not in the script's exact format"},
		{"trailing whitespace on a key line", func(t *testing.T, dir string) {
			editPin(t, dir, "tag spec/test-20260101\n", "tag spec/test-20260101 \n")
		}, "not in the script's exact format"},
		{"blank line among the values", func(t *testing.T, dir string) {
			editPin(t, dir, "tag spec/test-20260101\n", "tag spec/test-20260101\n\n")
		}, "is not a \"key value\" line"},
		{"size written +12", func(t *testing.T, dir string) {
			editFile(t, filepath.Join(dir, "bdp.pin"), func(s string) string {
				var out []string
				for _, l := range strings.Split(s, "\n") {
					if strings.HasSuffix(l, " LICENSE") {
						l = strings.Replace(l, " 12 ", " +12 ", 1)
					}
					out = append(out, l)
				}
				return strings.Join(out, "\n")
			})
		}, "not in the script's exact format"},
		{"wrong repository", func(t *testing.T, dir string) {
			editPin(t, dir, "repository "+pinRepository, "repository https://github.com/donnabox/bdp")
		}, "repository \"https://github.com/donnabox/bdp\""},
		{"wrong rule string", func(t *testing.T, dir string) {
			editPin(t, dir, "; not --first-parent)", "; --first-parent)")
		}, "spec-revision-rule"},
		{"tag not under spec/", func(t *testing.T, dir string) {
			editPin(t, dir, "tag spec/test-20260101\n", "tag v1\n")
		}, "not a bdp checkpoint tag"},
		{"git blob id swapped", func(t *testing.T, dir string) {
			editPin(t, dir, " "+gitBlobID([]byte("MIT License\n"))+" LICENSE\n", " "+strings.Repeat("e", 40)+" LICENSE\n")
		}, "spec/LICENSE git blob"},
		{"file listed twice", func(t *testing.T, dir string) {
			line := fileLine("LICENSE", []byte("MIT License\n"))
			editPin(t, dir, line+"\n", line+"\n"+line+"\n")
		}, "\"LICENSE\" is listed twice"},
		{"lock missing", func(t *testing.T, dir string) {
			editPin(t, dir, pinLockLine(t, dir)+"\n", "")
		}, "missing key \"lock\""},
		{"lock not a sha256", func(t *testing.T, dir string) {
			editPin(t, dir, pinLockLine(t, dir)+"\n", "lock "+strings.Repeat("d", 40)+"\n")
		}, "is not a 64-hex sha256"},
		{"stale lock", func(t *testing.T, dir string) {
			editPin(t, dir, pinLockLine(t, dir)+"\n", "lock "+strings.Repeat("d", 64)+"\n")
		}, "does not match its other non-comment lines"},
		// What a pin merged from two script runs looks like: a consumer slice's
		// file and its line, self-consistent but taken at another checkpoint,
		// next to the other run's lines and lock. Without the lock check every
		// other check passes it.
		{"file and line added without rerunning the script", func(t *testing.T, dir string) {
			raw := []byte("{\"from\": \"another checkpoint\"}\n")
			writeSpecFile(t, dir, "fixtures/x.json", raw)
			license := fileLine("LICENSE", []byte("MIT License\n"))
			editPin(t, dir, license+"\n", license+"\n"+fileLine("fixtures/x.json", raw)+"\n")
		}, "does not match its other non-comment lines"},
		{"pin missing", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "bdp.pin")); err != nil {
				t.Fatal(err)
			}
		}, "reading bdp.pin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, base)
			tc.mutate(t, dir)
			errs := checkVendored(dir)
			if tc.want == "" {
				for _, err := range errs {
					t.Errorf("unexpected: %v", err)
				}
				return
			}
			for _, err := range errs {
				if strings.Contains(err.Error(), tc.want) {
					return
				}
			}
			t.Errorf("want an error containing %q, got %v", tc.want, errs)
		})
	}
}

func TestLockOfMatchesSha256sum(t *testing.T) {
	// `printf 'a\nlock x\nb\n' | grep -v -e '^#' -e '^lock ' | sha256sum`:
	// the lock line is skipped and every other line ends in a newline.
	const want = "911169ddaaf146aff539f58c26c489af3b892dff0fe283c1c264c65ae5aa59a2"
	if got := lockOf([]string{"a", "lock x", "b"}); got != want {
		t.Errorf("lockOf = %s, want %s", got, want)
	}
}

func TestGitBlobIDMatchesGit(t *testing.T) {
	// `printf 'hello\n' | git hash-object --stdin` and the empty blob.
	for in, want := range map[string]string{
		"hello\n": "ce013625030ba8dba906f756967f9e9ca394464a",
		"":        "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
	} {
		if got := gitBlobID([]byte(in)); got != want {
			t.Errorf("gitBlobID(%q) = %s, want %s", in, got, want)
		}
	}
}
