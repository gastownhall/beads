//go:build cgo

// RunE-level tests for `bd remember`'s bare-key routing (GH#7275): a bare
// argument naming an exact key is a READ even when the key does not round-trip
// through memoryapi.DeriveKey (nine-plus segments, over 60 bytes, or dots), a
// miss is refused without writing anything, and a derived-key collision is
// only ever a hint. These drive the real command against a recording fake of
// the memory role, so they pin the routing — which keys were read, whether
// anything was written, what came back — with no database; the embedded lane
// pins the same facts against real storage.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/memoryops"
)

// bareKeyShapesGH7275 are the three argument shapes whose read intent the old
// round-trip gate (DeriveKey(insight) == insight) missed: the derivation keeps
// eight segments, caps at 60 bytes and collapses dots, so none of these keys
// survives its own derivation.
var bareKeyShapesGH7275 = []string{
	"one-two-three-four-five-six-seven-eight-nine", // nine segments; DeriveKey keeps the first eight
	strings.Repeat("k", 61),                        // one segment past the 60-byte cap
	"config.v2",                                    // DeriveKey collapses the dot to a hyphen
}

// errUnexpectedRemember is the sentinel a recording role returns when a test
// that must not write sees a Remember request: the command surfaces it as
// "storing memory: ...", failing the test loudly instead of letting an
// unwanted write land silently.
var errUnexpectedRemember = errors.New("test: unexpected bd remember write")

// recordingMemories is a map-backed memoryops.Memories that records the exact
// order of its Recall calls and every Remember request it receives, and
// mirrors the real role's key resolution (memoryapi.ResolveKey) so the
// validation refusals the command relays are the role's own sentences.
type recordingMemories struct {
	rows map[string]string

	// remembers holds every Remember request in arrival order; the tests
	// assert on it, not on the stored map, so a request that failed
	// validation stays visible.
	remembers []memoryops.RememberRequest
	// recallKeys holds the key of every Recall in arrival order.
	recallKeys []string
	// recallErr, when non-nil, fails the recall of the key it names, standing
	// in for a store read error at either probe.
	recallErr map[string]error
	// rememberErr, when non-nil, fails every write before it lands; tests
	// that expect a write set it to nil and let the rows update.
	rememberErr error
}

func (m *recordingMemories) Remember(_ context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	m.remembers = append(m.remembers, req)
	if m.rememberErr != nil {
		return memoryops.RememberResult{}, m.rememberErr
	}
	key, err := memoryapi.ResolveKey(req.Key, req.Content)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	_, replaced := m.rows[key]
	m.rows[key] = req.Content
	return memoryops.RememberResult{Key: key, Value: req.Content, Replaced: replaced}, nil
}

func (m *recordingMemories) Recall(_ context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	m.recallKeys = append(m.recallKeys, req.Key)
	if err := m.recallErr[req.Key]; err != nil {
		return memoryops.RecallResult{}, err
	}
	value, found := m.rows[req.Key]
	return memoryops.RecallResult{Key: req.Key, Value: value, Found: found}, nil
}

func (m *recordingMemories) Forget(_ context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	value, found := m.rows[req.Key]
	delete(m.rows, req.Key)
	return memoryops.ForgetResult{Key: req.Key, Value: value, Found: found}, nil
}

func (m *recordingMemories) List(_ context.Context, _ memoryops.ListRequest) (memoryops.ListResult, error) {
	out := make(map[string]string, len(m.rows))
	for k, v := range m.rows {
		out[k] = v
	}
	return memoryops.ListResult{Memories: out}, nil
}

// recordingMemoryStore is the store the remember tests run against: the
// memory role plus the user-kv commit a direct memory write performs
// (CommitConfigUserKVOnly), counted.
type recordingMemoryStore struct {
	storage.DoltStorage
	memories *recordingMemories
	commits  int
}

func (s *recordingMemoryStore) Memories() (memoryops.Memories, error) {
	return s.memories, nil
}

func (s *recordingMemoryStore) CommitConfigUserKVOnly(context.Context, string) error {
	s.commits++
	return nil
}

// setupRememberCommand prepares a real rememberCmd.RunE dispatch against a
// recording role, the way TestMemoryCommandsWarnAndSucceedWhenTheCommitIsRefused
// does: fresh globals, direct mode on an active fake store, the corpus budget
// off. Writes are refused by default (errUnexpectedRemember); a test that
// expects a write sets rememberErr = nil itself.
func setupRememberCommand(t *testing.T) (*recordingMemories, *recordingMemoryStore) {
	t.Helper()
	t.Setenv("BEADS_DIR", t.TempDir())
	saveAndRestoreGlobals(t)
	saveStorageMode(t)
	serverMode = true
	m := &recordingMemories{rows: map[string]string{}, rememberErr: errUnexpectedRemember}
	fake := &recordingMemoryStore{memories: m}
	store = fake
	setStoreActive(true)
	savedKey, savedJSON, savedConfigInt, savedForce := memoryKeyFlag, jsonOutput, memoryConfigInt, memoryForceFlag
	t.Cleanup(func() {
		memoryKeyFlag, jsonOutput, memoryConfigInt, memoryForceFlag = savedKey, savedJSON, savedConfigInt, savedForce
	})
	memoryKeyFlag = ""
	memoryForceFlag = false
	memoryConfigInt = func(string) int { return 0 }
	return m, fake
}

// runRemember drives one real `bd remember <insight>` dispatch (no --key).
func runRemember(insight string) error {
	return rememberCmd.RunE(rememberCmd, []string{insight})
}

// parseRememberJSON decodes one JSON object the command printed.
func parseRememberJSON(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &body); err != nil {
		t.Fatalf("--json output is not one JSON object: %v\n%s", err, out)
	}
	return body
}

// exitOne reports whether err is the command's exit-1 error.
func exitOne(t *testing.T, err error) bool {
	t.Helper()
	ee, ok := err.(*exitError)
	if !ok || ee.Code != 1 {
		t.Errorf("err = %v, want *exitError{Code: 1}", err)
		return false
	}
	return true
}

// TestRememberBareKeyReadsExactKey pins the read half of the GH#7275 fix: a
// bare argument naming an EXISTING exact key recalls that key's own value,
// even when the key does not round-trip through DeriveKey. At base the gate
// derived the argument and required the derivation back, so every shape here
// fell through to Remember and stored the key as its own content — this test
// fails on every shape there.
func TestRememberBareKeyReadsExactKey(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		// The GH#7275 shapes plus one canonical short slug, which the old
		// round-trip gate already read: the fix must keep those reads too.
		for _, key := range append(bareKeyShapesGH7275, "auth-flow") {
			t.Run(fmt.Sprintf("json=%v/%s", asJSON, key), func(t *testing.T) {
				m, fake := setupRememberCommand(t)
				jsonOutput = asJSON
				content := "exact memory content for " + key
				m.rows[key] = content
				if d := memoryapi.DeriveKey(key); d != key {
					m.rows[d] = "derived collision content"
				}

				var runErr error
				stdout, stderr := captureStdio(t, func() { runErr = runRemember(key) })
				if runErr != nil {
					t.Fatalf("bd remember %q = %v, want the exact-key recall to succeed", key, runErr)
				}
				if len(m.remembers) != 0 {
					t.Errorf("bd remember %q issued %d Remember request(s), want none: %+v", key, len(m.remembers), m.remembers)
				}
				if fake.commits != 0 {
					t.Errorf("bd remember %q committed %d time(s), want none", key, fake.commits)
				}
				// An exact hit must not even ask about the derived key.
				if !reflect.DeepEqual(m.recallKeys, []string{key}) {
					t.Errorf("bd remember %q read %v, want exactly the exact key", key, m.recallKeys)
				}
				if asJSON {
					body := parseRememberJSON(t, stdout)
					if body["key"] != key || body["value"] != content || body["found"] != true || body["action"] != "recalled" {
						t.Errorf("--json recall = %v, want key %q, value %q, found true, action recalled", body, key, content)
					}
					if strings.Contains(stdout, "derived collision content") {
						t.Errorf("--json recall leaked the derived key's content:\n%s", stdout)
					}
				} else {
					if stdout != content+"\n" {
						t.Errorf("stdout = %q, want the exact key's content", stdout)
					}
					if !strings.Contains(stderr, "(recalled") {
						t.Errorf("stderr = %q, want the bare-existing-key READS notice", stderr)
					}
				}
			})
		}
	}

	// Presence is row existence, not a nonempty value: an exact row holding
	// the empty string is recalled (with the empty value) rather than falling
	// through to the nonempty derived key.
	t.Run("empty exact value beats a nonempty derived value", func(t *testing.T) {
		m, fake := setupRememberCommand(t)
		jsonOutput = true
		key := "config.v2"
		derived := memoryapi.DeriveKey(key)
		m.rows[key] = ""
		m.rows[derived] = "derived collision content"

		var runErr error
		stdout, _ := captureStdio(t, func() { runErr = runRemember(key) })
		if runErr != nil {
			t.Fatalf("bd remember %q = %v, want the empty-valued recall to succeed", key, runErr)
		}
		body := parseRememberJSON(t, stdout)
		if body["found"] != true || body["value"] != "" {
			t.Errorf("--json recall = %v, want found true with the empty value", body)
		}
		if !reflect.DeepEqual(m.recallKeys, []string{key}) {
			t.Errorf("bd remember %q read %v, want exactly the exact key", key, m.recallKeys)
		}
		if len(m.remembers) != 0 || fake.commits != 0 {
			t.Errorf("recall wrote: remembers %+v, commits %d", m.remembers, fake.commits)
		}
	})

	// A read error at the exact probe surfaces the shipped "recalling memory"
	// error and writes nothing.
	t.Run("exact-probe read error exits without writing", func(t *testing.T) {
		for _, asJSON := range []bool{false, true} {
			m, fake := setupRememberCommand(t)
			jsonOutput = asJSON
			m.recallErr = map[string]error{"config.v2": errors.New("read blew up")}

			var runErr error
			stdout, stderr := captureStdio(t, func() { runErr = runRemember("config.v2") })
			if !exitOne(t, runErr) {
				t.Fatal("command did not refuse")
			}
			if asJSON {
				body := parseRememberJSON(t, stdout)
				if !strings.Contains(fmt.Sprint(body["error"]), "recalling memory: read blew up") {
					t.Errorf("--json error = %v, want the recalling memory failure", body["error"])
				}
			} else if !strings.Contains(stderr, "recalling memory: read blew up") {
				t.Errorf("stderr = %q, want the recalling memory failure", stderr)
			}
			if len(m.remembers) != 0 || fake.commits != 0 {
				t.Errorf("failed read wrote: remembers %+v, commits %d", m.remembers, fake.commits)
			}
		}
	})
}

// TestRememberBareKeyRefusesMissingWithoutWriting pins the refusal half for
// the GH#7275 shapes: a bare key naming nothing (neither the exact key nor its
// derivation) exits 1 with the shipped bare-key refusal, emits no hint, and
// creates or changes no memory. At base these arguments wrote the key as its
// own content under its derived key.
func TestRememberBareKeyRefusesMissingWithoutWriting(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		for _, key := range bareKeyShapesGH7275 {
			t.Run(fmt.Sprintf("json=%v/%s", asJSON, key), func(t *testing.T) {
				m, fake := setupRememberCommand(t)
				jsonOutput = asJSON
				// The corpus starts and stays empty: neither the exact key
				// nor its derivation exists.

				var runErr error
				stdout, stderr := captureStdio(t, func() { runErr = runRemember(key) })
				exitOne(t, runErr)
				if len(m.remembers) != 0 {
					t.Errorf("bd remember %q issued %d Remember request(s), want none: %+v", key, len(m.remembers), m.remembers)
				}
				if len(m.rows) != 0 {
					t.Errorf("bd remember %q stored %v, want nothing", key, m.rows)
				}
				if fake.commits != 0 {
					t.Errorf("bd remember %q committed %d time(s), want none", key, fake.commits)
				}
				if asJSON {
					body := parseRememberJSON(t, stdout)
					if !strings.Contains(fmt.Sprint(body["error"]), fmt.Sprintf("no memory named %q", key)) {
						t.Errorf("--json error = %v, want the bare-key refusal naming %q", body["error"], key)
					}
					if _, hasHint := body["hint"]; hasHint {
						t.Errorf("--json refusal = %v, want no hint when no derived key exists", body)
					}
				} else {
					if !strings.Contains(stderr, fmt.Sprintf("no memory named %q", key)) {
						t.Errorf("stderr = %q, want the bare-key refusal naming %q", stderr, key)
					}
					if strings.Contains(stderr, "Hint:") {
						t.Errorf("stderr = %q, want no hint when no derived key exists", stderr)
					}
					if stdout != "" {
						t.Errorf("stdout = %q, want the refusal to write nothing there", stdout)
					}
				}
			})
		}
	}
}

// TestRememberBareKeySuggestsDerivedKey pins the collision hint: the exact key
// is absent but its DERIVED key exists, and the refusal names that derived key
// and its recall command — as a hint, never as the answer. At base the
// command took the write path and overwrote the derived key's memory with the
// requested key as content.
func TestRememberBareKeySuggestsDerivedKey(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		for _, key := range bareKeyShapesGH7275 {
			t.Run(fmt.Sprintf("json=%v/%s", asJSON, key), func(t *testing.T) {
				m, fake := setupRememberCommand(t)
				jsonOutput = asJSON
				derived := memoryapi.DeriveKey(key)
				m.rows[derived] = "derived collision content"
				wantHint := fmt.Sprintf("did you mean %s? A memory exists under that derived key; to read it, run: bd recall %s", derived, derived)

				var runErr error
				stdout, stderr := captureStdio(t, func() { runErr = runRemember(key) })
				exitOne(t, runErr)
				if len(m.remembers) != 0 {
					t.Errorf("bd remember %q issued %d Remember request(s), want none: %+v", key, len(m.remembers), m.remembers)
				}
				if fake.commits != 0 {
					t.Errorf("bd remember %q committed %d time(s), want none", key, fake.commits)
				}
				if m.rows[derived] != "derived collision content" {
					t.Errorf("derived memory = %q, want it untouched", m.rows[derived])
				}
				// The exact key first, then the derived probe.
				if !reflect.DeepEqual(m.recallKeys, []string{key, derived}) {
					t.Errorf("bd remember %q read %v, want the exact key then the derived one", key, m.recallKeys)
				}
				if asJSON {
					body := parseRememberJSON(t, stdout)
					if !strings.Contains(fmt.Sprint(body["error"]), fmt.Sprintf("no memory named %q", key)) {
						t.Errorf("--json error = %v, want the bare-key refusal naming %q", body["error"], key)
					}
					if body["hint"] != wantHint {
						t.Errorf("--json hint = %v, want %q", body["hint"], wantHint)
					}
				} else {
					if !strings.Contains(stderr, fmt.Sprintf("no memory named %q", key)) {
						t.Errorf("stderr = %q, want the bare-key refusal naming %q", stderr, key)
					}
					if !strings.Contains(stderr, "Hint: "+wantHint) {
						t.Errorf("stderr = %q, want the hint %q", stderr, "Hint: "+wantHint)
					}
					if stdout != "" {
						t.Errorf("stdout = %q, want the refusal to write nothing there", stdout)
					}
				}
				if strings.Contains(stdout, "derived collision content") || strings.Contains(stderr, "derived collision content") {
					t.Errorf("the hint must not substitute the derived memory's content:\nstdout: %s\nstderr: %s", stdout, stderr)
				}
			})
		}
	}

	// A derived row holding the empty string is still a row: the hint names
	// it, presence being row existence here too.
	t.Run("empty derived value still hints", func(t *testing.T) {
		m, _ := setupRememberCommand(t)
		jsonOutput = true
		key := "config.v2"
		derived := memoryapi.DeriveKey(key)
		m.rows[derived] = ""

		var runErr error
		stdout, _ := captureStdio(t, func() { runErr = runRemember(key) })
		exitOne(t, runErr)
		body := parseRememberJSON(t, stdout)
		wantHint := fmt.Sprintf("did you mean %s? A memory exists under that derived key; to read it, run: bd recall %s", derived, derived)
		if body["hint"] != wantHint {
			t.Errorf("--json hint = %v, want %q for an empty-valued derived row", body["hint"], wantHint)
		}
	})

	// A read error at the derived-key probe surfaces the same "recalling
	// memory" error, and neither error falls through to a write.
	t.Run("derived-probe read error exits without writing", func(t *testing.T) {
		m, fake := setupRememberCommand(t)
		key := "config.v2"
		derived := memoryapi.DeriveKey(key)
		m.recallErr = map[string]error{derived: errors.New("read blew up")}

		var runErr error
		stdout, stderr := captureStdio(t, func() { runErr = runRemember(key) })
		exitOne(t, runErr)
		if !strings.Contains(stdout+stderr, "recalling memory: read blew up") {
			t.Errorf("output = %q / %q, want the recalling memory failure", stdout, stderr)
		}
		if len(m.remembers) != 0 || fake.commits != 0 {
			t.Errorf("failed read wrote: remembers %+v, commits %d", m.remembers, fake.commits)
		}
	})
}

// TestRememberExplicitKeyWritesUnrestrictedKeys pins that --key keeps stating
// write intent for the keys the bare-key grammar would read: long, dotted and
// otherwise unrestricted keys store the content under the exact key given,
// including when the content is itself a bare key. The write contract is the
// shipped one; this guards the fix against swallowing explicit writes.
func TestRememberExplicitKeyWritesUnrestrictedKeys(t *testing.T) {
	keys := append(append([]string{}, bareKeyShapesGH7275...), "Some_Key", "trail-")
	for _, asJSON := range []bool{false, true} {
		for _, key := range keys {
			t.Run(fmt.Sprintf("json=%v/%s", asJSON, key), func(t *testing.T) {
				m, fake := setupRememberCommand(t)
				m.rememberErr = nil // a write is the expected outcome
				memoryKeyFlag = key
				jsonOutput = asJSON
				content := "deliberate insight under an explicit key"

				var runErr error
				stdout, _ := captureStdio(t, func() { runErr = runRemember(content) })
				if runErr != nil {
					t.Fatalf("bd remember --key %q = %v, want the explicit write to succeed", key, runErr)
				}
				if len(m.remembers) != 1 || m.remembers[0] != (memoryops.RememberRequest{Key: key, Content: content}) {
					t.Errorf("Remember requests = %+v, want exactly one storing %q under %q", m.remembers, content, key)
				}
				if m.rows[key] != content {
					t.Errorf("stored under %q = %q, want %q", key, m.rows[key], content)
				}
				if fake.commits != 1 {
					t.Errorf("commits = %d, want one for the write", fake.commits)
				}
				if !asJSON {
					if !strings.Contains(stdout, "Remembered ["+key+"]") {
						t.Errorf("stdout = %q, want the write to name the explicit key", stdout)
					}
				}
			})
		}
	}

	// Slug-like CONTENT with an explicit key is a write, never a read.
	t.Run("bare-slug content with an explicit key stores", func(t *testing.T) {
		m, fake := setupRememberCommand(t)
		m.rememberErr = nil
		memoryKeyFlag = "config.v2"

		var runErr error
		captureStdio(t, func() { runErr = runRemember("auth-flow") })
		if runErr != nil {
			t.Fatalf("bd remember auth-flow --key config.v2 = %v, want a write", runErr)
		}
		if len(m.remembers) != 1 || m.remembers[0] != (memoryops.RememberRequest{Key: "config.v2", Content: "auth-flow"}) {
			t.Errorf("Remember requests = %+v, want the slug stored as content under config.v2", m.remembers)
		}
		if m.rows["config.v2"] != "auth-flow" {
			t.Errorf("stored under config.v2 = %q, want the slug content", m.rows["config.v2"])
		}
		if fake.commits != 1 {
			t.Errorf("commits = %d, want one for the write", fake.commits)
		}
	})
}
