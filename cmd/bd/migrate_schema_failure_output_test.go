package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// schemaRepairStub is a DoltStorage stand-in for `bd migrate schema`: its
// explicit migrate-and-repair (storage.SchemaPostConditionRepairer) returns a
// canned result and error.
type schemaRepairStub struct {
	storage.DoltStorage
	res           storage.SchemaRepairResult
	err           error
	migrateCalled bool
}

func (s *schemaRepairStub) ApplySchemaMigrations(context.Context) (int, error) {
	s.migrateCalled = true
	return 0, errors.New("ApplySchemaMigrations called on a backend that can repair")
}

func (s *schemaRepairStub) ApplySchemaMigrationsAndRepair(context.Context) (storage.SchemaRepairResult, error) {
	return s.res, s.err
}

// runSchemaMigrateAgainst runs handleSchemaMigrate in a fresh workspace whose
// store is stub, and returns what it wrote: stdout with --json, stderr
// without.
func runSchemaMigrateAgainst(t *testing.T, stub *schemaRepairStub, asJSON bool) string {
	t.Helper()
	saveAndRestoreGlobals(t)
	isolateBeadsDirForTest(t)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	oldJSON, oldWrote := jsonOutput, commandDidWrite.Load()
	t.Cleanup(func() {
		jsonOutput = oldJSON
		commandDidWrite.Store(oldWrote)
	})
	jsonOutput = asJSON
	store = stub

	var out string
	var err error
	if asJSON {
		out, err = captureStdoutWithError(t, handleSchemaMigrate)
	} else {
		out = captureStderr(t, func() { err = handleSchemaMigrate() })
	}
	if err == nil {
		t.Fatalf("handleSchemaMigrate succeeded although the repair failed; output:\n%s", out)
	}
	if stub.migrateCalled {
		t.Fatalf("handleSchemaMigrate used ApplySchemaMigrations on a backend that can repair")
	}
	return out
}

// GH#5269: a `bd migrate schema` that fails after part of its work landed
// says what landed. --json carries `applied`, `repaired` and `uncommitted`
// (always arrays); the text error names the finished repairs, and a repair
// whose commit failed is reported as uncommitted with the commit that
// finishes it, never as repaired.
func TestSchemaMigrateFailureReportsWhatLanded_5269(t *testing.T) {
	type jsonFailure struct {
		Error       string   `json:"error"`
		Message     string   `json:"message"`
		Applied     int      `json:"applied"`
		Repaired    []string `json:"repaired"`
		Uncommitted []string `json:"uncommitted"`
	}
	uncommittedErr := &schema.PostConditionRepairUncommittedError{
		Columns: []string{"dependencies.id"},
		Tables:  []string{"dependencies"},
		Err:     errors.New("committing repair: i/o timeout"),
	}
	for _, tc := range []struct {
		name     string
		res      storage.SchemaRepairResult
		err      error
		wantJSON jsonFailure
		wantText []string
		notText  string
	}{
		{
			name: "repair finished, then an error",
			res:  storage.SchemaRepairResult{Repaired: []string{"dependencies.id"}},
			err:  errors.New("boom"),
			wantJSON: jsonFailure{
				Error: "schema_migration_failed", Message: "boom",
				Repaired: []string{"dependencies.id"}, Uncommitted: []string{},
			},
			wantText: []string{"Error: schema migration failed after repairing dependencies.id: boom"},
		},
		{
			name: "tracked repair not committed",
			res:  storage.SchemaRepairResult{Repaired: []string{"events.id"}, Uncommitted: []string{"dependencies.id"}},
			err:  uncommittedErr,
			wantJSON: jsonFailure{
				Error: "schema_migration_failed", Message: uncommittedErr.Error(),
				Repaired: []string{"events.id"}, Uncommitted: []string{"dependencies.id"},
			},
			wantText: []string{
				"schema migration failed after repairing events.id",
				"dropped the default(s) on dependencies.id but did not commit the change",
				"will not find it again",
				"bd dolt commit",
			},
			notText: "repairing dependencies.id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runSchemaMigrateAgainst(t, &schemaRepairStub{res: tc.res, err: tc.err}, true)
			var got jsonFailure
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("parse --json output %q: %v", out, err)
			}
			if !reflect.DeepEqual(got, tc.wantJSON) {
				t.Fatalf("--json = %+v, want %+v", got, tc.wantJSON)
			}

			text := runSchemaMigrateAgainst(t, &schemaRepairStub{res: tc.res, err: tc.err}, false)
			for _, want := range tc.wantText {
				if !strings.Contains(text, want) {
					t.Errorf("text error does not contain %q:\n%s", want, text)
				}
			}
			if tc.notText != "" && strings.Contains(text, tc.notText) {
				t.Errorf("text error calls an uncommitted repair repaired (%q):\n%s", tc.notText, text)
			}
		})
	}
}
