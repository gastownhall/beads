//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/memoryops"
)

// errUnexpectedMemoryWrite is what bareKeyMemories answers a Remember with
// when the test expects none: the write never lands, so a read-shaped call
// that falls through to it fails without touching the map it reads from.
var errUnexpectedMemoryWrite = errors.New("unexpected memory write")

// bareKeyMemories is a map-backed memory plane that records every Recall key,
// in order, and every Remember request. It refuses writes unless allowWrites
// is set, and then lands them the way every role does, under ResolveKey's key.
type bareKeyMemories struct {
	memoryops.Memories
	stored      map[string]string
	recallErr   map[string]error
	allowWrites bool
	recalls     []string
	writes      []memoryops.RememberRequest
}

func (m *bareKeyMemories) Recall(_ context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	m.recalls = append(m.recalls, req.Key)
	if err := m.recallErr[req.Key]; err != nil {
		return memoryops.RecallResult{}, err
	}
	value, found := m.stored[req.Key]
	return memoryops.RecallResult{Key: req.Key, Value: value, Found: found}, nil
}

func (m *bareKeyMemories) Remember(_ context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	m.writes = append(m.writes, req)
	if !m.allowWrites {
		return memoryops.RememberResult{}, errUnexpectedMemoryWrite
	}
	key, err := memoryapi.ResolveKey(req.Key, req.Content)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	_, replaced := m.stored[key]
	m.stored[key] = req.Content
	return memoryops.RememberResult{Key: key, Value: req.Content, Replaced: replaced}, nil
}

// bareKeyStore is a SQL-server store whose only live parts are the memory
// plane and the memory commit, which it counts.
type bareKeyStore struct {
	storage.DoltStorage
	memories *bareKeyMemories
	commits  int
}

func (s *bareKeyStore) Memories() (memoryops.Memories, error) { return s.memories, nil }

func (s *bareKeyStore) CommitConfigUserKVOnly(context.Context, string) error {
	s.commits++
	return nil
}

// bareKeyRun is one `bd remember` invocation through the real RunE.
type bareKeyRun struct {
	stdout, stderr string
	err            error
	commits        int
}

// runRememberCmd runs `bd remember <insight> [--key key]` against mem with the
// budget off, the way TestMemoryCommandsWarnAndSucceedWhenTheCommitIsRefused
// does, and restores every global it sets.
func runRememberCmd(t *testing.T, mem *bareKeyMemories, insight, key string, asJSON bool) bareKeyRun {
	t.Helper()
	t.Setenv("BEADS_DIR", t.TempDir())
	saveAndRestoreGlobals(t)
	saveStorageMode(t)
	serverMode = true
	fake := &bareKeyStore{memories: mem}
	store = fake
	setStoreActive(true)
	savedKey, savedJSON, savedConfigInt := memoryKeyFlag, jsonOutput, memoryConfigInt
	t.Cleanup(func() { memoryKeyFlag, jsonOutput, memoryConfigInt = savedKey, savedJSON, savedConfigInt })
	memoryKeyFlag, jsonOutput = key, asJSON
	memoryConfigInt = func(string) int { return 0 }

	var run bareKeyRun
	run.stdout, run.stderr = captureStdio(t, func() { run.err = rememberCmd.RunE(rememberCmd, []string{insight}) })
	run.commits = fake.commits
	return run
}

// decodeJSONBody decodes one JSON object from stdout, unwrapping the
// {schema_version, data} envelope when it is on.
func decodeJSONBody(t *testing.T, stdout string) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if data, ok := body["data"].(map[string]interface{}); ok {
		return data
	}
	return body
}

// bareKeyShapes are the read-shaped keys that do not round-trip through
// DeriveKey (GH#7275): more than eight segments, more than 60 bytes, a dot.
// The long key is one segment, so the 60-byte cap, not the eight-segment
// cutoff, is what changes its derivation.
var bareKeyShapes = []struct{ name, key string }{
	{"nine segments", "one-two-three-four-five-six-seven-eight-nine"},
	{"over 60 bytes", strings.Repeat("k", 61)},
	{"dotted", "config.v2"},
}

// derivedKeyOf is DeriveKey(key), failing the test when the shape does round
// trip, since every bareKeyShapes case exists because it does not.
func derivedKeyOf(t *testing.T, key string) string {
	t.Helper()
	derived := memoryapi.DeriveKey(key)
	if derived == "" || derived == key {
		t.Fatalf("DeriveKey(%q) = %q: the fixture needs a key that derives to a different, non-empty key", key, derived)
	}
	return derived
}

// bareKeyCaseName names a subtest for its output mode and what it varies.
func bareKeyCaseName(asJSON bool, what string) string {
	if asJSON {
		return "json " + what
	}
	return "text " + what
}

// TestRememberBareKeyReadsExactKey pins GH#7275's read: a bare key that does
// not round-trip through DeriveKey recalls ITS OWN memory, even when another
// memory sits under the derived key, and writes nothing. At the base the gate
// fell through and stored the key as content under the derived key.
func TestRememberBareKeyReadsExactKey(t *testing.T) {
	for _, shape := range bareKeyShapes {
		t.Run(shape.name, func(t *testing.T) {
			for _, asJSON := range []bool{false, true} {
				for _, exactValue := range []string{"the exact memory", ""} {
					t.Run(bareKeyCaseName(asJSON, "exact value "+strconv.Quote(exactValue)), func(t *testing.T) {
						key := shape.key
						derived := derivedKeyOf(t, key)
						mem := &bareKeyMemories{stored: map[string]string{key: exactValue, derived: "the derived memory"}}
						before := maps.Clone(mem.stored)

						run := runRememberCmd(t, mem, key, "", asJSON)
						if run.err != nil {
							t.Fatalf("bd remember %s = %v, want a recall\nstdout:\n%s\nstderr:\n%s", key, run.err, run.stdout, run.stderr)
						}
						if len(mem.writes) != 0 || run.commits != 0 {
							t.Fatalf("bd remember %s wrote %v (%d commits), want a read only", key, mem.writes, run.commits)
						}
						if !maps.Equal(mem.stored, before) {
							t.Errorf("corpus changed: %v, want %v", mem.stored, before)
						}
						if len(mem.recalls) != 1 || mem.recalls[0] != key {
							t.Errorf("recalled %q, want exactly [%q]: an exact hit must not read the derived key", mem.recalls, key)
						}
						if asJSON {
							body := decodeJSONBody(t, run.stdout)
							if body["key"] != key || body["value"] != exactValue || body["found"] != true || body["action"] != "recalled" {
								t.Errorf("body = %v, want key %q, value %q, found true, action recalled", body, key, exactValue)
							}
							return
						}
						if run.stdout != exactValue+"\n" {
							t.Errorf("stdout = %q, want the exact memory %q", run.stdout, exactValue+"\n")
						}
						if strings.Contains(run.stdout+run.stderr, "the derived memory") {
							t.Errorf("output carries the derived key's memory:\nstdout:\n%s\nstderr:\n%s", run.stdout, run.stderr)
						}
					})
				}
			}
		})
	}

	// A canonical short key, which the old round-trip gate already read: the
	// fix must keep those reads too.
	for _, asJSON := range []bool{false, true} {
		t.Run(bareKeyCaseName(asJSON, "canonical key"), func(t *testing.T) {
			const key = "auth-flow"
			if derived := memoryapi.DeriveKey(key); derived != key {
				t.Fatalf("DeriveKey(%q) = %q: the control needs a key that round-trips", key, derived)
			}
			mem := &bareKeyMemories{stored: map[string]string{key: "the exact memory"}}
			before := maps.Clone(mem.stored)

			run := runRememberCmd(t, mem, key, "", asJSON)
			if run.err != nil {
				t.Fatalf("bd remember %s = %v, want a recall\nstdout:\n%s\nstderr:\n%s", key, run.err, run.stdout, run.stderr)
			}
			if len(mem.writes) != 0 || run.commits != 0 || !maps.Equal(mem.stored, before) {
				t.Fatalf("bd remember %s wrote %v (%d commits), want a read only", key, mem.writes, run.commits)
			}
			if !slices.Equal(mem.recalls, []string{key}) {
				t.Errorf("recalled %q, want exactly [%q]", mem.recalls, key)
			}
			if asJSON {
				body := decodeJSONBody(t, run.stdout)
				if body["key"] != key || body["value"] != "the exact memory" || body["found"] != true || body["action"] != "recalled" {
					t.Errorf("body = %v, want key %q, value %q, found true, action recalled", body, key, "the exact memory")
				}
				return
			}
			if run.stdout != "the exact memory\n" {
				t.Errorf("stdout = %q, want the exact memory", run.stdout)
			}
		})
	}
}

// bareKeyRefusal is the shipped refusal for a bare key naming nothing,
// spelled as rememberBareKeyPath spells it.
func bareKeyRefusal(key string) string {
	keyArg, keyNote := remedy.CommandArg(runtime.GOOS, key, "<key>")
	return fmt.Sprintf("no memory named %q to recall -- and refusing to store a bare key-like token as its own content. "+
		"bd remember WRITES (its positional arg is CONTENT, not a key). "+
		"To browse keys: bd memories. To store it anyway, run: bd remember %q --key %s%s",
		key, key, keyArg, keyNote)
}

// assertBareKeyRefused checks a run exited 1 having written nothing.
func assertBareKeyRefused(t *testing.T, run bareKeyRun, mem *bareKeyMemories, before map[string]string) {
	t.Helper()
	var exit *exitError
	if !errors.As(run.err, &exit) || exit.Code != 1 {
		t.Fatalf("err = %v, want exit 1\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
	}
	if len(mem.writes) != 0 || run.commits != 0 {
		t.Errorf("wrote %v (%d commits), want nothing written", mem.writes, run.commits)
	}
	if !maps.Equal(mem.stored, before) {
		t.Errorf("corpus changed: %v, want %v", mem.stored, before)
	}
}

// TestRememberBareKeyMissRefusesWithoutWriting pins the other half of
// GH#7275: a bare key naming nothing -- neither itself nor its derived key --
// is refused with the shipped refusal and no hint, and nothing is stored. At
// the base it was stored as content under the derived key.
func TestRememberBareKeyMissRefusesWithoutWriting(t *testing.T) {
	for _, shape := range bareKeyShapes {
		t.Run(shape.name, func(t *testing.T) {
			for _, asJSON := range []bool{false, true} {
				t.Run(bareKeyCaseName(asJSON, "miss"), func(t *testing.T) {
					key := shape.key
					derivedKeyOf(t, key)
					mem := &bareKeyMemories{stored: map[string]string{"unrelated": "an unrelated memory"}}
					before := maps.Clone(mem.stored)

					run := runRememberCmd(t, mem, key, "", asJSON)
					assertBareKeyRefused(t, run, mem, before)
					want := bareKeyRefusal(key)
					if asJSON {
						body := decodeJSONBody(t, run.stdout)
						if body["error"] != want {
							t.Errorf("error = %q\nwant    %q", body["error"], want)
						}
						if hint, ok := body["hint"]; ok {
							t.Errorf("hint = %q, want none: no memory sits under the derived key", hint)
						}
						return
					}
					if run.stderr != "Error: "+want+"\n" {
						t.Errorf("stderr = %q\nwant     %q", run.stderr, "Error: "+want+"\n")
					}
					if run.stdout != "" {
						t.Errorf("stdout = %q, want nothing", run.stdout)
					}
				})
			}
		})
	}
}

// derivedKeyHint is the hint a refused bare key gets when a memory sits under
// its derived key.
func derivedKeyHint(derived string) string {
	return "did you mean " + derived + "? A memory exists under that derived key; to read it, run: bd recall " + derived
}

// TestRememberBareKeyMissHintsDerivedKey pins the suggestion: a bare key
// naming nothing whose DERIVED key names a memory is still refused -- the
// derived memory answers a different question -- but the refusal points at
// it, in a separate hint, without ever printing its content. A memory stored
// as the empty string is present (#5963), so it is suggested too.
func TestRememberBareKeyMissHintsDerivedKey(t *testing.T) {
	const derivedValue = "the derived memory, never printed"
	for _, shape := range bareKeyShapes {
		t.Run(shape.name, func(t *testing.T) {
			for _, asJSON := range []bool{false, true} {
				for _, value := range []string{derivedValue, ""} {
					t.Run(bareKeyCaseName(asJSON, "derived value "+strconv.Quote(value)), func(t *testing.T) {
						key := shape.key
						derived := derivedKeyOf(t, key)
						mem := &bareKeyMemories{stored: map[string]string{derived: value}}
						before := maps.Clone(mem.stored)

						run := runRememberCmd(t, mem, key, "", asJSON)
						assertBareKeyRefused(t, run, mem, before)
						if want := []string{key, derived}; !slices.Equal(mem.recalls, want) {
							t.Errorf("recalled %q, want %q", mem.recalls, want)
						}
						if strings.Contains(run.stdout+run.stderr, derivedValue) {
							t.Errorf("output carries the derived key's memory:\nstdout:\n%s\nstderr:\n%s", run.stdout, run.stderr)
						}
						refusal, hint := bareKeyRefusal(key), derivedKeyHint(derived)
						if asJSON {
							body := decodeJSONBody(t, run.stdout)
							if body["error"] != refusal {
								t.Errorf("error = %q\nwant    %q", body["error"], refusal)
							}
							if body["hint"] != hint {
								t.Errorf("hint = %q\nwant   %q", body["hint"], hint)
							}
							return
						}
						if want := "Error: " + refusal + "\nHint: " + hint + "\n"; run.stderr != want {
							t.Errorf("stderr = %q\nwant     %q", run.stderr, want)
						}
						if run.stdout != "" {
							t.Errorf("stdout = %q, want nothing", run.stdout)
						}
					})
				}
			}
		})
	}
}

// TestRememberBareKeyReadErrorsNeverWrite pins that a failed read at either
// probe -- the exact key or the derived key the hint asks about -- is
// reported as the shipped read error and never falls through to a write.
func TestRememberBareKeyReadErrorsNeverWrite(t *testing.T) {
	const key = "config.v2"
	derived := derivedKeyOf(t, key)
	boom := errors.New("boom")
	for _, tc := range []struct {
		name        string
		failing     string
		wantRecalls []string
	}{
		{"exact probe", key, []string{key}},
		{"derived probe", derived, []string{key, derived}},
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(bareKeyCaseName(asJSON, tc.name), func(t *testing.T) {
				mem := &bareKeyMemories{
					stored:    map[string]string{derived: "the derived memory"},
					recallErr: map[string]error{tc.failing: boom},
				}
				before := maps.Clone(mem.stored)

				run := runRememberCmd(t, mem, key, "", asJSON)
				assertBareKeyRefused(t, run, mem, before)
				if !slices.Equal(mem.recalls, tc.wantRecalls) {
					t.Errorf("recalled %q, want %q", mem.recalls, tc.wantRecalls)
				}
				if got := run.stdout + run.stderr; !strings.Contains(got, "recalling memory: boom") {
					t.Errorf("output does not report the read error:\n%s", got)
				}
			})
		}
	}
}

// assertRememberWrote checks a run stored content under key with exactly the
// request wantReq and printed the shipped write result.
func assertRememberWrote(t *testing.T, run bareKeyRun, mem *bareKeyMemories, asJSON bool, wantReq memoryops.RememberRequest, key, content string) {
	t.Helper()
	if run.err != nil {
		t.Fatalf("err = %v, want the write\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
	}
	if want := []memoryops.RememberRequest{wantReq}; !slices.Equal(mem.writes, want) {
		t.Errorf("writes = %v, want %v", mem.writes, want)
	}
	if run.commits != 1 {
		t.Errorf("commits = %d, want one for the write", run.commits)
	}
	if len(mem.recalls) != 0 {
		t.Errorf("recalled %q, want no read on the write path", mem.recalls)
	}
	if got, ok := mem.stored[key]; !ok || got != content {
		t.Errorf("stored[%q] = %q (present %v), want %q", key, got, ok, content)
	}
	if asJSON {
		body := decodeJSONBody(t, run.stdout)
		if body["key"] != key || body["value"] != content {
			t.Errorf("body = %v, want key %q, value %q", body, key, content)
		}
		return
	}
	if !strings.Contains(run.stdout, "["+key+"]: "+content) {
		t.Errorf("stdout = %q, want the write result for [%s]", run.stdout, key)
	}
}

// TestRememberExplicitKeyStoresVerbatim pins that --key keeps its write
// contract: the content is stored under the key as given, whatever its shape,
// even when the content is itself a bare key naming an existing memory.
func TestRememberExplicitKeyStoresVerbatim(t *testing.T) {
	keys := []string{
		"one-two-three-four-five-six-seven-eight-nine",
		"a-very-long-single-key-that-is-well-over-sixty-bytes-in-total",
		strings.Repeat("k", 61),
		"config.v2",
		"Mixed Case_key with spaces",
		"Some_Key",
		"trail-",
	}
	contents := []string{"config.v2", "the auth module uses JWT"}
	for _, key := range keys {
		for _, content := range contents {
			for _, asJSON := range []bool{false, true} {
				t.Run(bareKeyCaseName(asJSON, strconv.Quote(key)+" "+strconv.Quote(content)), func(t *testing.T) {
					// The content names an existing memory, which must neither
					// answer nor change.
					mem := &bareKeyMemories{allowWrites: true, stored: map[string]string{"config.v2": "existing"}}
					if key == "config.v2" {
						mem.stored = map[string]string{"config-v2": "existing"}
					}
					others := maps.Clone(mem.stored)

					run := runRememberCmd(t, mem, content, key, asJSON)
					assertRememberWrote(t, run, mem, asJSON, memoryops.RememberRequest{Key: key, Content: content}, key, content)
					for k, v := range others {
						if mem.stored[k] != v {
							t.Errorf("stored[%q] = %q, want it untouched (%q)", k, mem.stored[k], v)
						}
					}
				})
			}
		}
	}
}

// TestRememberNonKeyContentTakesContentPath pins that content outside the
// bare-key grammar keeps the content path, reading nothing first: prose and
// key-like-but-irregular tokens are stored under their derived key, and empty
// or underivable content keeps the role's validation refusal.
func TestRememberNonKeyContentTakesContentPath(t *testing.T) {
	for _, content := range []string{
		"always run tests with the -race flag",
		"Config.V2",
		"snake_case_word",
		"a--b",
		"trailing-",
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(bareKeyCaseName(asJSON, strconv.Quote(content)), func(t *testing.T) {
				mem := &bareKeyMemories{allowWrites: true, stored: map[string]string{}}
				run := runRememberCmd(t, mem, content, "", asJSON)
				derived := memoryapi.DeriveKey(content)
				assertRememberWrote(t, run, mem, asJSON, memoryops.RememberRequest{Content: content}, derived, content)
			})
		}
	}

	for _, tc := range []struct{ content, want string }{
		{"", "memory content cannot be empty"},
		{"   ", "memory content cannot be empty"},
		{".", "could not generate key from content; use --key to specify one"},
		{"-", "could not generate key from content; use --key to specify one"},
		{"!!!", "could not generate key from content; use --key to specify one"},
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(bareKeyCaseName(asJSON, strconv.Quote(tc.content)), func(t *testing.T) {
				mem := &bareKeyMemories{allowWrites: true, stored: map[string]string{}}
				run := runRememberCmd(t, mem, tc.content, "", asJSON)
				var exit *exitError
				if !errors.As(run.err, &exit) || exit.Code != 1 {
					t.Fatalf("err = %v, want exit 1", run.err)
				}
				if len(mem.recalls) != 0 {
					t.Errorf("recalled %q, want the content path's refusal with no read", mem.recalls)
				}
				if len(mem.stored) != 0 {
					t.Errorf("stored %v, want nothing", mem.stored)
				}
				if asJSON {
					if body := decodeJSONBody(t, run.stdout); body["error"] != tc.want {
						t.Errorf("error = %q, want %q", body["error"], tc.want)
					}
					return
				}
				if want := "Error: " + tc.want + "\n"; run.stderr != want {
					t.Errorf("stderr = %q, want %q", run.stderr, want)
				}
			})
		}
	}
}
