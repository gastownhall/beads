package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// GH#7921: --metadata stored any top-level key, while --set-metadata,
// --unset-metadata, the metadata filters and the query DSL all refuse a key
// outside storage.ValidateMetadataKey's grammar. A key like "bad:key" was
// therefore written once and could never be filtered on, set or removed from
// the CLI again. create and update now refuse it before the write.
func TestReadMetadataFlagValidatesKeysGH7921(t *testing.T) {
	for _, value := range []string{
		`{"bad:key":"v"}`,
		`{"1st":"v"}`,
		`{"a b":"v"}`,
		`{"":"v"}`,
		`{"good_key":"v","bad:key":"v"}`,
	} {
		t.Run(value, func(t *testing.T) {
			_, err := readMetadataFlag(value)
			if err == nil {
				t.Fatalf("readMetadataFlag(%s) accepted a key outside the metadata key grammar", value)
			}
			if !strings.Contains(err.Error(), "invalid --metadata: invalid metadata key") {
				t.Fatalf("readMetadataFlag(%s) error = %q, want the metadata key message", value, err)
			}
		})
	}

	// Keys inside the grammar pass, values stay opaque, and only top-level
	// keys are metadata keys: a nested object's keys are part of the value.
	for _, value := range []string{
		`{"good_key":"v","jira.sprint":1,"jira/sprint":"x","_k":null}`,
		`{"outer":{"inner:key":"v"}}`,
	} {
		got, err := readMetadataFlag(value)
		if err != nil {
			t.Fatalf("readMetadataFlag(%s): %v", value, err)
		}
		if string(got) != value {
			t.Fatalf("readMetadataFlag(%s) = %s, want it unchanged", value, got)
		}
	}

	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"bad:key":"v"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMetadataFlag("@" + path); err == nil || !strings.Contains(err.Error(), "invalid metadata key") {
		t.Fatalf("@file with a bad key: error = %v, want the metadata key message", err)
	}
}

// Every top-level key is checked, not just the first one in sorted order, and
// when several are out of grammar the error names the first in sorted order,
// so the message does not depend on Go's random map iteration. The loop gives
// an unsorted walk many chances to start somewhere else.
func TestMetadataFlagKeyOrderGH7921(t *testing.T) {
	if _, err := readMetadataFlag(`{"a_ok":"v","z:bad":"v"}`); err == nil || !strings.Contains(err.Error(), `invalid metadata key "z:bad"`) {
		t.Fatalf("a bad key sorted after a good one: error = %v, want z:bad refused", err)
	}

	members := []string{`"a_ok":"v"`}
	for _, key := range []string{"m:bad", "z:bad", "b:bad", "q:bad", "f:bad", "x:bad", "k:bad"} {
		members = append(members, `"`+key+`":"v"`)
	}
	value := "{" + strings.Join(members, ",") + "}"
	for i := 0; i < 64; i++ {
		_, err := readMetadataFlag(value)
		if err == nil || !strings.Contains(err.Error(), `invalid metadata key "b:bad"`) {
			t.Fatalf("run %d: error = %v, want the first sorted bad key b:bad", i, err)
		}
	}
}

func TestInputBadMetadataKeyGH7921(t *testing.T) {
	createCmd := newCreateFlagsCommand(t, "--title", "Title", "--metadata", `{"bad:key":"v"}`)
	var err error
	stderr := captureStderr(t, func() {
		_, err = gatherCreateInput(createCmd, nil)
	})
	if err == nil || !strings.Contains(stderr, `invalid metadata key "bad:key"`) {
		t.Fatalf("gatherCreateInput: err = %v, stderr = %q, want the bad key refused", err, stderr)
	}

	updateCmd := &cobra.Command{Use: "update"}
	updateCmd.Flags().String("metadata", "", "Set custom metadata")
	if err := updateCmd.ParseFlags([]string{"--metadata", `{"bad:key":"v"}`}); err != nil {
		t.Fatalf("parse update flags: %v", err)
	}
	stderr = captureStderr(t, func() {
		_, err = gatherUpdateInput(context.Background(), updateCmd)
	})
	if err == nil || !strings.Contains(stderr, `invalid metadata key "bad:key"`) {
		t.Fatalf("gatherUpdateInput: err = %v, stderr = %q, want the bad key refused", err, stderr)
	}
}
