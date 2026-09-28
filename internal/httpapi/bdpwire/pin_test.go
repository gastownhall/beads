package bdpwire

import (
	"bufio"
	"bytes"
	"crypto/sha1" //nolint:gosec // git blob identity is sha1 by definition; it is an identifier here, not a security primitive
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// schema/PROVENANCE is the vendoring record and this file is what makes it binding.
// Every vendored file is listed with its sha256 and, for a verbatim upstream
// file, its git blob sha1 — the identity `git hash-object` and the GitHub
// trees API report, so anyone can check the vendored bytes against the pinned
// commit without this package's help. Both digests are recomputed from the
// bytes on disk here; a fixture edited in place, a file added without an
// entry, or an entry with no file all fail. The derived entries (the spec's
// JSON example fences) are anchored to the spec file's own blob sha1
// (`spec-blob:`) and each to its fence's line range, so they are not
// self-referential: TestDerivedEntriesReproduceFromTheSpec re-extracts every
// one from a local copy of the spec when BDP_SPEC_AT_PIN names one. Nothing
// here touches the network; that test skips rather than fetch.

type pinEntry struct {
	sha256   string
	local    string
	upstream string
	blob     string
}

type pinFile struct {
	header  map[string]string
	entries []pinEntry
}

func loadPin(t *testing.T) pinFile {
	t.Helper()
	f, err := os.Open(filepath.Join(schemaDir, "PROVENANCE"))
	if err != nil {
		t.Fatalf("open PROVENANCE: %v", err)
	}
	defer f.Close()

	pin := pinFile{header: map[string]string{}}
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) == 2 && strings.HasSuffix(fields[0], ":") {
			pin.header[strings.TrimSuffix(fields[0], ":")] = fields[1]
			continue
		}
		if len(fields) != 4 {
			t.Fatalf("PROVENANCE line %d: want `sha256 local upstream blob`, got %q", line, text)
		}
		pin.entries = append(pin.entries, pinEntry{fields[0], fields[1], fields[2], fields[3]})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read PROVENANCE: %v", err)
	}
	return pin
}

func gitBlobSHA1(data []byte) string {
	h := sha1.New() //nolint:gosec // see the import comment
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// specFenceRef is the upstream column of a derived entry:
// docs/specs/bdp.md#L<opener>-L<closer>.
var specFenceRef = regexp.MustCompile(`^docs/specs/bdp\.md#L([1-9][0-9]*)-L([1-9][0-9]*)$`)

var lowerHex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestPinHeaderNamesThePinnedCommitAndBundle(t *testing.T) {
	pin := loadPin(t)
	if got := pin.header["commit"]; got != Pin {
		t.Errorf("PROVENANCE commit = %q, Pin const = %q: the two must name the same upstream commit", got, Pin)
	}
	if got := pin.header["schema-id"]; got != SchemaID {
		t.Errorf("PROVENANCE schema-id = %q, SchemaID const = %q", got, SchemaID)
	}
	if got := pin.header["upstream"]; got != "https://github.com/gastownhall/bdp" {
		t.Errorf("PROVENANCE upstream = %q", got)
	}
	// The derived entries' source file is identified by its own blob sha1, so
	// the 13 example files are anchored to something other than themselves.
	if got := pin.header["spec"]; got != "docs/specs/bdp.md" {
		t.Errorf("PROVENANCE spec = %q, want docs/specs/bdp.md", got)
	}
	if got := pin.header["spec-blob"]; !lowerHex40.MatchString(got) {
		t.Errorf("PROVENANCE spec-blob = %q, want the spec file's 40-hex git blob sha1", got)
	}
}

// fenceRange parses a derived entry's upstream column and checks it against
// the file name's leading number (the opener line).
func fenceRange(t *testing.T, e pinEntry) (opener, closer int) {
	t.Helper()
	m := specFenceRef.FindStringSubmatch(e.upstream)
	if m == nil {
		t.Fatalf("%s: a derived entry must name its fence's line range as docs/specs/bdp.md#L<opener>-L<closer>, got %q", e.local, e.upstream)
	}
	opener, _ = strconv.Atoi(m[1])
	closer, _ = strconv.Atoi(m[2])
	if closer <= opener+1 {
		t.Fatalf("%s: fence range %s is empty", e.local, e.upstream)
	}
	base := strings.TrimPrefix(e.local, "spec-examples/")
	if !strings.HasPrefix(base, fmt.Sprintf("%04d-", opener)) {
		t.Fatalf("%s: the file name's leading number must be the fence opener line %d", e.local, opener)
	}
	return opener, closer
}

// TestDerivedEntriesReproduceFromTheSpec re-extracts every derived entry from
// a local copy of docs/specs/bdp.md at the pinned commit: the copy's git blob
// sha1 must equal the spec-blob header, each entry's fence must open with
// ```json on its opener line and close with ``` on its closer line, and the
// lines between them plus one newline must hash to the entry's sha256. It is
// gated on BDP_SPEC_AT_PIN (a path) so the suite stays offline; a re-pin runs
// it once against the fetched spec and records the result in the commit.
func TestDerivedEntriesReproduceFromTheSpec(t *testing.T) {
	path := os.Getenv("BDP_SPEC_AT_PIN")
	if path == "" {
		t.Skip("set BDP_SPEC_AT_PIN to a local copy of docs/specs/bdp.md at the pinned commit to reproduce the derived entries")
	}
	spec, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pin := loadPin(t)
	if got, want := gitBlobSHA1(spec), pin.header["spec-blob"]; got != want {
		t.Fatalf("%s: git blob sha1 %s, PROVENANCE spec-blob says %s: not the spec at the pinned commit", path, got, want)
	}
	lines := strings.Split(string(spec), "\n")
	derived := 0
	for _, e := range pin.entries {
		if e.blob != "-" {
			continue
		}
		derived++
		opener, closer := fenceRange(t, e)
		if closer > len(lines) {
			t.Errorf("%s: fence closer line %d is past the end of the spec (%d lines)", e.local, closer, len(lines))
			continue
		}
		if got := lines[opener-1]; !strings.HasPrefix(got, "```json") {
			t.Errorf("%s: line %d is %q, want a ```json fence opener", e.local, opener, got)
		}
		if got := strings.TrimSpace(lines[closer-1]); got != "```" {
			t.Errorf("%s: line %d is %q, want the ``` fence closer", e.local, closer, got)
		}
		body := strings.Join(lines[opener:closer-1], "\n") + "\n"
		sum := sha256.Sum256([]byte(body))
		if got := hex.EncodeToString(sum[:]); got != e.sha256 {
			t.Errorf("%s: fence L%d-L%d hashes to %s, PROVENANCE says %s", e.local, opener, closer, got, e.sha256)
		}
		if disk := readSchemaFile(t, e.local); string(disk) != body {
			t.Errorf("%s: the vendored file differs from the fence at L%d-L%d", e.local, opener, closer)
		}
	}
	if derived == 0 {
		t.Fatal("PROVENANCE lists no derived entries")
	}
	t.Logf("reproduced %d derived entries from %s (blob %s)", derived, path, pin.header["spec-blob"])
}

func TestEveryVendoredFileIsPinnedAndUnchanged(t *testing.T) {
	pin := loadPin(t)

	listed := map[string]pinEntry{}
	for _, e := range pin.entries {
		if _, dup := listed[e.local]; dup {
			t.Errorf("PROVENANCE lists %s twice", e.local)
		}
		listed[e.local] = e
	}

	onDisk := map[string]bool{}
	err := filepath.WalkDir(schemaDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(schemaDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "PROVENANCE" {
			return nil
		}
		onDisk[rel] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", schemaDir, err)
	}

	if unlisted := diff(onDisk, listed); len(unlisted) > 0 {
		t.Errorf("files under schema/ with no PROVENANCE entry: %v\nevery vendored file is pinned by sha256 — add a line to schema/PROVENANCE with its provenance", unlisted)
	}
	if missing := diff(listed, onDisk); len(missing) > 0 {
		t.Errorf("PROVENANCE entries with no file on disk: %v", missing)
	}

	for _, e := range pin.entries {
		if !onDisk[e.local] {
			continue
		}
		data := readSchemaFile(t, e.local)
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != e.sha256 {
			t.Errorf("%s: sha256 %s, PROVENANCE says %s\nthe vendored bytes changed; re-vendor from the pinned commit or re-pin deliberately (GENERATOR.md, Re-pinning)", e.local, got, e.sha256)
		}
		if e.blob == "-" {
			fenceRange(t, e)
			continue
		}
		if got := gitBlobSHA1(data); got != e.blob {
			t.Errorf("%s: git blob sha1 %s, PROVENANCE says %s (upstream %s)", e.local, got, e.blob, e.upstream)
		}
	}
}

func TestBundleEntryIsTheNormativeArtifact(t *testing.T) {
	pin := loadPin(t)
	var bundle *pinEntry
	for i := range pin.entries {
		if pin.entries[i].local == "bdp-v0.schema.json" {
			bundle = &pin.entries[i]
		}
	}
	if bundle == nil {
		t.Fatal("PROVENANCE has no entry for bdp-v0.schema.json")
	}
	// The spec fixes the artifact's path in the upstream repository; the pin
	// must say it came from there and not from some copy.
	if bundle.upstream != "schemas/bdp-v0.schema.json" {
		t.Errorf("bundle upstream path = %q, want schemas/bdp-v0.schema.json", bundle.upstream)
	}

	onDisk := readSchemaFile(t, "bdp-v0.schema.json")
	embedded := SchemaBundle()
	if !bytes.Equal(onDisk, embedded) {
		t.Fatal("SchemaBundle() differs from schema/bdp-v0.schema.json: the embed directive points somewhere else")
	}

	doc := asMap(t, decodeAny(t, embedded), "bundle")
	if got := doc["$id"]; got != SchemaID {
		t.Errorf("bundle $id = %v, SchemaID = %q", got, SchemaID)
	}
	if got := doc["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("bundle $schema = %v, want JSON Schema 2020-12", got)
	}
}

func TestSchemaBundleReturnsACopy(t *testing.T) {
	first := SchemaBundle()
	first[0] = 'x'
	if second := SchemaBundle(); second[0] != '{' {
		t.Fatal("SchemaBundle() handed out the embedded bytes themselves; a caller could corrupt them for everyone")
	}
}
