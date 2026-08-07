package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPromoteEvidenceCopiesValidatedRegularFileByteForByte(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping complete-census validation in short mode")
	}

	root := t.TempDir()
	catalog := "../release-catalog.json"
	source := filepath.Join(root, "raw.json")
	destination := filepath.Join(root, "trusted.json")
	diagnostic := filepath.Join(root, "diagnostic.json")
	raw := validPromotionCensus(t)
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"promote-evidence", catalog, source, destination, diagnostic}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("promoted evidence = %q, want byte-for-byte %q", got, raw)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("promoted evidence mode = %s, want regular", info.Mode())
	}
	if _, err := os.Lstat(diagnostic); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful promotion retained diagnostic: %v", err)
	}
}

func TestPromoteEvidenceRejectsInvalidCensusWithoutPromotingAttackerBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping complete-census validation in short mode")
	}

	tests := []struct {
		name string
		raw  func(*testing.T) []byte
	}{
		{
			name: "invalid JSON",
			raw:  func(*testing.T) []byte { return []byte(`{"hostile":"do not copy me"}\n`) },
		},
		{
			name: "catalog mismatch",
			raw: func(t *testing.T) []byte {
				result, err := decodeCensus(validPromotionCensus(t))
				if err != nil {
					t.Fatal(err)
				}
				result.CatalogSHA256 = strings.Repeat("0", 64)
				raw, err := encodeCensus(result)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			},
		},
		{
			name: "incomplete census",
			raw: func(t *testing.T) []byte {
				result, err := decodeCensus(validPromotionCensus(t))
				if err != nil {
					t.Fatal(err)
				}
				result.Observations = result.Observations[:len(result.Observations)-1]
				raw, err := encodeCensus(result)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			},
		},
		{
			name: "unroutable topology",
			raw: func(t *testing.T) []byte {
				result, err := decodeCensus(validPromotionCensus(t))
				if err != nil {
					t.Fatal(err)
				}
				for index := range result.Families {
					if result.Families[index].Mode != "sqlite" {
						continue
					}
					var layout map[string]json.RawMessage
					if err := json.Unmarshal(result.Families[index].Layout, &layout); err != nil {
						t.Fatal(err)
					}
					layout["topology"] = json.RawMessage(`["database:.beads/beads.db","unroutable:marker"]`)
					updated, err := json.Marshal(layout)
					if err != nil {
						t.Fatal(err)
					}
					result.Families[index].Layout = updated
					id, err := familyIDFromCanonicalLayout(result.Families[index].Mode, updated)
					if err != nil {
						t.Fatal(err)
					}
					oldID := result.Families[index].ID
					result.Families[index].ID = id
					for observation := range result.Observations {
						if result.Observations[observation].FamilyID == oldID {
							result.Observations[observation].FamilyID = id
						}
					}
					break
				}
				sort.Slice(result.Families, func(i, j int) bool { return result.Families[i].ID < result.Families[j].ID })
				raw, err := encodeCensus(result)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "raw.json")
			destination := filepath.Join(root, "trusted.json")
			diagnostic := filepath.Join(root, "diagnostic.json")
			raw := test.raw(t)
			if err := os.WriteFile(source, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, []byte("stale trusted data"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := promoteEvidence("../release-catalog.json", source, destination, diagnostic)
			assertEvidencePromotionRejection(t, err, destination, diagnostic, "census-invalid")
			diagnosticRaw, err := os.ReadFile(diagnostic)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(diagnosticRaw, []byte("hostile")) || bytes.Contains(diagnosticRaw, raw) {
				t.Fatalf("diagnostic leaked untrusted census data: %q", diagnosticRaw)
			}
		})
	}
}

func TestPromoteEvidenceRejectsNonRegularFiles(t *testing.T) {
	tests := []struct {
		name   string
		source func(*testing.T, string) string
	}{
		{
			name: "symlink",
			source: func(t *testing.T, path string) string {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target.json")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "directory",
			source: func(t *testing.T, path string) string {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "device",
			source: func(t *testing.T, _ string) string {
				t.Helper()
				info, err := os.Lstat("/dev/null")
				if err != nil {
					t.Skipf("inspect /dev/null: %v", err)
				}
				if info.Mode().IsRegular() {
					t.Skip("/dev/null is unexpectedly regular")
				}
				return "/dev/null"
			},
		},
		{
			name: "socket",
			source: func(t *testing.T, path string) string {
				t.Helper()
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := listener.Close(); err != nil {
						t.Errorf("close Unix listener: %v", err)
					}
				})
				return path
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := test.source(t, filepath.Join(root, "raw.json"))
			destination := filepath.Join(root, "trusted.json")
			diagnostic := filepath.Join(root, "diagnostic.json")
			if err := os.WriteFile(destination, []byte("stale trusted data"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := promoteEvidence("../release-catalog.json", source, destination, diagnostic)
			assertEvidencePromotionRejection(t, err, destination, diagnostic, "source-not-regular")
		})
	}
}

func TestPromoteEvidenceRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "raw.json")
	destination := filepath.Join(root, "trusted.json")
	diagnostic := filepath.Join(root, "diagnostic.json")
	if err := unix.Mkfifo(source, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- promoteEvidence("../release-catalog.json", source, destination, diagnostic)
	}()
	select {
	case err := <-result:
		assertEvidencePromotionRejection(t, err, destination, diagnostic, "source-not-regular")
	case <-time.After(time.Second):
		t.Fatal("promotion blocked while inspecting FIFO evidence")
	}
}

func TestPromoteEvidenceRejectsEmptyAndOversizedFiles(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		create func(*testing.T, string)
	}{
		{
			name:   "empty",
			reason: "source-empty",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "oversized",
			reason: "source-too-large",
			create: func(t *testing.T, path string) {
				t.Helper()
				file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maxPromotedEvidenceBytes + 1); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "raw.json")
			destination := filepath.Join(root, "trusted.json")
			diagnostic := filepath.Join(root, "diagnostic.json")
			test.create(t, source)

			err := promoteEvidence("../release-catalog.json", source, destination, diagnostic)
			assertEvidencePromotionRejection(t, err, destination, diagnostic, test.reason)
		})
	}
}

func TestReadEvidenceForPromotionRejectsPostReadPathReplacement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "raw.json")
	replacement := filepath.Join(root, "replacement.json")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := readEvidenceForPromotionWithHook(source, func() error {
		return os.Rename(replacement, source)
	})
	if got := evidencePromotionRejectionReason(err); got != "source-identity-changed" {
		t.Fatalf("replacement error = %v, reason = %q, want source-identity-changed", err, got)
	}
}

func TestPromoteEvidenceRejectionWritesBoundedDiagnostic(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "missing.json")
	destination := filepath.Join(root, "trusted.json")
	diagnostic := filepath.Join(root, "diagnostic.json")

	err := promoteEvidence("../release-catalog.json", source, destination, diagnostic)
	assertEvidencePromotionRejection(t, err, destination, diagnostic, "source-inspection-failed")
	raw, err := os.ReadFile(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxEvidencePromotionDiagnosticBytes {
		t.Fatalf("diagnostic size = %d, want at most %d", len(raw), maxEvidencePromotionDiagnosticBytes)
	}
	var got evidencePromotionDiagnostic
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode diagnostic: %v", err)
	}
	if got.SchemaVersion != 1 || got.Status != "rejected" || got.Reason != "source-inspection-failed" {
		t.Fatalf("diagnostic = %#v", got)
	}
}

func TestRunContainerPromotesLaneEvidenceInsideTrustedContainers(t *testing.T) {
	raw, err := os.ReadFile("run-container.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		`readonly CENSUS_ONE_DIAGNOSTIC="$OUTPUT_DIR/census-1-diagnostic.json"`,
		`readonly CENSUS_TWO_DIAGNOSTIC="$OUTPUT_DIR/census-2-diagnostic.json"`,
		`declare -A GENERATE_STATUSES PROMOTION_STATUSES CACHE_STATUSES CLEANUP_STATUSES`,
		`src=$evidence_dir,dst=/evidence,readonly`,
		`src=$OUTPUT_DIR,dst=/output`,
		`/control/census promote-evidence scripts/migration-test/release-catalog.json /evidence/census.json`,
		`PROMOTION_STATUSES["$lane"]=$promotion_status`,
		`"$CENSUS_ONE_DIAGNOSTIC" "$CENSUS_TWO_DIAGNOSTIC"`,
		`"$CENSUS_ONE_EVIDENCE" "$CENSUS_ONE_CACHE_SAFE_MARKER"`,
		`"census-1.json" "census-1-diagnostic.json"`,
		`"$CENSUS_TWO_EVIDENCE" "$CENSUS_TWO_CACHE_SAFE_MARKER"`,
		`"census-2.json" "census-2-diagnostic.json"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("run-container.sh lacks evidence-promotion invariant %q", required)
		}
	}
	for _, forbidden := range []string{
		`cp -f "$CENSUS_ONE_EVIDENCE/census.json"`,
		`cp -f "$CENSUS_TWO_EVIDENCE/census.json"`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("run-container.sh still host-copies raw evidence via %q", forbidden)
		}
	}

	generate := strings.Index(script, `/control/census generate`)
	promote := strings.Index(script, `/control/census promote-evidence`)
	cacheValidate := strings.Index(script, `/control/census cache-validate`)
	if generate < 0 || promote < 0 || cacheValidate < 0 || generate > promote || promote > cacheValidate {
		t.Error("trusted evidence promotion does not run immediately after generation and before cache validation")
	}
	promotionStart := strings.LastIndex(script[:promote], "if docker run")
	if promotionStart < 0 {
		t.Fatal("could not find trusted promotion container")
	}
	promotionContainer := script[promotionStart:promote]
	for _, required := range []string{
		`src=$PROJECT_ROOT,dst=/workspace,readonly`,
		`src=$CONTROL_DIR,dst=/control,readonly`,
		`src=$evidence_dir,dst=/evidence,readonly`,
		`src=$OUTPUT_DIR,dst=/output`,
	} {
		if !strings.Contains(promotionContainer, required) {
			t.Errorf("trusted promotion container lacks mount %q", required)
		}
	}
	if strings.Contains(promotionContainer, `src=$OUTPUT_DIR,dst=/output,readonly`) {
		t.Error("trusted promotion container cannot write promoted evidence or diagnostics")
	}

	generatorFailure := strings.Index(script, `if [ "${GENERATE_STATUSES[$lane]}" -ne 0 ]; then`)
	promotionFailure := strings.Index(script, `if [ "${PROMOTION_STATUSES[$lane]}" -ne 0 ]; then`)
	if generatorFailure < 0 || promotionFailure < 0 || generatorFailure > promotionFailure {
		t.Error("promotion failure masks the original generator failure")
	}
	laneGeneratorFailure := strings.Index(script, `if [ "$generate_status" -ne 0 ]; then`)
	lanePromotionFailure := strings.Index(script, `if [ "$promotion_status" -ne 0 ]; then`)
	laneCleanupResult := strings.Index(script, `return "$cleanup_status"`)
	if laneGeneratorFailure < 0 || lanePromotionFailure < 0 || laneCleanupResult < 0 ||
		laneGeneratorFailure > lanePromotionFailure || lanePromotionFailure > laneCleanupResult {
		t.Error("lane result does not fail successful generation on rejected promotion while preserving generator precedence")
	}
}

func assertEvidencePromotionRejection(t *testing.T, err error, destination, diagnostic, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("evidence promotion unexpectedly succeeded")
	}
	if got := evidencePromotionRejectionReason(err); got != reason {
		t.Fatalf("rejection reason = %q, want %q (error: %v)", got, reason, err)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected promotion retained trusted output: %v", statErr)
	}
	raw, readErr := os.ReadFile(diagnostic)
	if readErr != nil {
		t.Fatalf("read rejection diagnostic: %v", readErr)
	}
	var got evidencePromotionDiagnostic
	if decodeErr := json.Unmarshal(raw, &got); decodeErr != nil {
		t.Fatalf("decode rejection diagnostic: %v", decodeErr)
	}
	if got.Status != "rejected" || got.Reason != reason {
		t.Fatalf("diagnostic = %#v, want rejection reason %q", got, reason)
	}
}

func validPromotionCensus(t *testing.T) []byte {
	t.Helper()
	raw, err := readGzip("testdata/runtime-schema-census.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
