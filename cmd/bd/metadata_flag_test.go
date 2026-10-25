package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// GH#6035: --metadata only checked json.Valid, so `bd create --metadata
// '"oops"'` stored a bare JSON string as the metadata, while `bd update`
// with the same value failed late in the storage merge. Both commands now
// require a top-level JSON object up front.
const metadataObjectErr = "invalid --metadata: must be a JSON object"

var nonObjectMetadata = []struct {
	value string
	kind  string
}{
	{`"oops"`, "a JSON string"},
	{`[1,2]`, "a JSON array"},
	{`42`, "a JSON number"},
	{`true`, "a JSON boolean"},
	{`null`, "JSON null"},
}

func TestReadMetadataFlagRequiresObjectGH6035(t *testing.T) {
	for _, tt := range nonObjectMetadata {
		t.Run(tt.value, func(t *testing.T) {
			_, err := readMetadataFlag(tt.value)
			if err == nil {
				t.Fatalf("readMetadataFlag(%s) accepted a non-object", tt.value)
			}
			if !strings.Contains(err.Error(), metadataObjectErr) || !strings.Contains(err.Error(), tt.kind) {
				t.Fatalf("readMetadataFlag(%s) error = %q, want %q naming %q", tt.value, err, metadataObjectErr, tt.kind)
			}
		})
	}

	// Objects pass through verbatim, including a string value that happens
	// to look like JSON: values inside the object are opaque.
	for _, value := range []string{`{}`, `{"a":1}`, ` {"mykey":"{\"inner\":\"v\"}"} `} {
		got, err := readMetadataFlag(value)
		if err != nil {
			t.Fatalf("readMetadataFlag(%s): %v", value, err)
		}
		if string(got) != value {
			t.Fatalf("readMetadataFlag(%s) = %s, want it unchanged", value, got)
		}
	}

	if _, err := readMetadataFlag(`{"a":`); err == nil || !strings.Contains(err.Error(), "invalid JSON in --metadata") {
		t.Fatalf("malformed JSON error = %v, want the invalid-JSON message", err)
	}

	dir := t.TempDir()
	objPath := filepath.Join(dir, "obj.json")
	strPath := filepath.Join(dir, "str.json")
	if err := os.WriteFile(objPath, []byte(`{"from":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strPath, []byte(`"oops"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readMetadataFlag("@" + objPath); err != nil || string(got) != `{"from":"file"}` {
		t.Fatalf("@file object = %s, %v", got, err)
	}
	if _, err := readMetadataFlag("@" + strPath); err == nil || !strings.Contains(err.Error(), metadataObjectErr) {
		t.Fatalf("@file string error = %v, want %q", err, metadataObjectErr)
	}
}

func TestGatherCreateInputRejectsNonObjectMetadataGH6035(t *testing.T) {
	for _, tt := range nonObjectMetadata {
		t.Run(tt.value, func(t *testing.T) {
			cmd := newCreateFlagsCommand(t, "--title", "Title", "--metadata", tt.value)
			var err error
			stderr := captureStderr(t, func() {
				_, err = gatherCreateInput(cmd, nil)
			})
			if err == nil {
				t.Fatalf("gatherCreateInput accepted --metadata %s", tt.value)
			}
			if !strings.Contains(stderr, metadataObjectErr) {
				t.Fatalf("stderr = %q, want %q", stderr, metadataObjectErr)
			}
		})
	}

	cmd := newCreateFlagsCommand(t, "--title", "Title", "--metadata", `{"a":1}`)
	in, err := gatherCreateInput(cmd, nil)
	if err != nil {
		t.Fatalf("gatherCreateInput with an object: %v", err)
	}
	if string(in.metadata) != `{"a":1}` || !in.metadataSet {
		t.Fatalf("metadata = %q, set = %t", in.metadata, in.metadataSet)
	}
}

func TestGatherUpdateInputRejectsNonObjectMetadataGH6035(t *testing.T) {
	newCmd := func(value string) *cobra.Command {
		cmd := &cobra.Command{Use: "update"}
		cmd.Flags().String("metadata", "", "Set custom metadata")
		if err := cmd.ParseFlags([]string{"--metadata", value}); err != nil {
			t.Fatalf("parse update flags: %v", err)
		}
		return cmd
	}

	for _, tt := range nonObjectMetadata {
		t.Run(tt.value, func(t *testing.T) {
			var err error
			stderr := captureStderr(t, func() {
				_, err = gatherUpdateInput(context.Background(), newCmd(tt.value))
			})
			if err == nil {
				t.Fatalf("gatherUpdateInput accepted --metadata %s", tt.value)
			}
			if !strings.Contains(stderr, metadataObjectErr) {
				t.Fatalf("stderr = %q, want %q", stderr, metadataObjectErr)
			}
		})
	}

	in, err := gatherUpdateInput(context.Background(), newCmd(`{"a":1}`))
	if err != nil {
		t.Fatalf("gatherUpdateInput with an object: %v", err)
	}
	if string(in.mergeMetadataIn) != `{"a":1}` {
		t.Fatalf("mergeMetadataIn = %q", in.mergeMetadataIn)
	}
}
