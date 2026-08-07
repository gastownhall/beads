package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunContainerIsolatesControlAndLaneEvidenceMounts(t *testing.T) {
	raw, err := os.ReadFile("run-container.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)

	for _, required := range []string{
		`CONTROL_DIR="$(mktemp -d `,
		`trap cleanup_control EXIT`,
		`-o /control/census`,
		`src=$CONTROL_DIR,dst=/control,readonly`,
		`src=$evidence_dir,dst=/evidence`,
		`src=$evidence_dir,dst=/evidence,readonly`,
		`/control/census generate`,
		`/evidence/census.json`,
		`/control/census promote-evidence scripts/migration-test/release-catalog.json /evidence/census.json`,
		`"/output/$trusted_output_name" "/output/$diagnostic_name"`,
		`/control/census cache-validate`,
		`/control/census seal`,
		"rm -f \\\n    \"$OUTPUT_DIR/census-1.json\" \"$OUTPUT_DIR/census-2.json\" \\",
		`clear_lane_evidence "$evidence_dir"`,
		`find /evidence -mindepth 1 -delete`,
		`cmp "$OUTPUT_DIR/census-1.json" "$OUTPUT_DIR/census-2.json"`,
		`sha256sum "$OUTPUT_DIR/census-1.json" "$OUTPUT_DIR/census-2.json"`,
		`/control/census seal scripts/migration-test/release-catalog.json /output/census-1.json`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("run-container.sh lacks isolation invariant %q", required)
		}
	}
	if got := strings.Count(script, `src=$OUTPUT_DIR,dst=/output`); got != 2 {
		t.Errorf("writable OUTPUT_DIR root mounts = %d, want trusted promoter and seal/verify mounts", got)
	}
	clearState := strings.Index(script, `clear_private_state "$state_dir"`)
	clearEvidence := strings.Index(script, `clear_lane_evidence "$evidence_dir"`)
	generate := strings.Index(script, `/control/census generate`)
	if clearState < 0 || clearEvidence < 0 || generate < 0 ||
		clearState > clearEvidence || clearEvidence > generate {
		t.Error("private lane state and only its prior evidence are not cleared immediately before historical generation")
	}
	for _, forbidden := range []string{
		`cp -f `,
		`$CENSUS_ONE_EVIDENCE/census.json`,
		`$CENSUS_TWO_EVIDENCE/census.json`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("host directly handles raw generator evidence through %q", forbidden)
		}
	}
	if got := strings.Count(script, `/evidence/census.json`); got != 2 {
		t.Errorf("raw in-container evidence references = %d, want generator output and trusted promoter input", got)
	}
	if got := strings.Count(script, `src=$evidence_dir,dst=/evidence,readonly`); got != 1 {
		t.Errorf("read-only trusted-promoter evidence mounts = %d, want one", got)
	}
	if got := strings.Count(script, `src=$evidence_dir,dst=/evidence"`); got != 2 {
		t.Errorf("writable lane evidence mounts = %d, want generator and trusted cleanup mounts", got)
	}
	if strings.Contains(script, `src=$cache_dir,dst=/cache,readonly`) {
		t.Error("cache validation cannot authenticate cleanup with a read-only cache mount")
	}
	if got := strings.Count(script, `src=$cache_dir,dst=/cache"`); got != 2 {
		t.Errorf("writable lane-cache mounts = %d, want generator and trusted cache-validator mounts", got)
	}
}

func TestRunContainerHardensOnlyHistoricalGenerator(t *testing.T) {
	raw, err := os.ReadFile("run-container.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)

	for _, required := range []string{
		`GENERATOR_UID="$(id -u)"`,
		`readonly GENERATOR_UID`,
		`GENERATOR_GID="$(id -g)"`,
		`readonly GENERATOR_GID`,
		`--init`,
		`--user "$GENERATOR_UID:$GENERATOR_GID"`,
		`--read-only`,
		`--cap-drop ALL`,
		`--security-opt no-new-privileges:true`,
		`# The two hardened lanes peaked at 417 PIDs and about 2.2 GiB.`,
		`--pids-limit 1024`,
		`--memory 8g`,
		`--memory-swap 8g`,
		`--cpus 4`,
		`--tmpfs /tmp:rw,nosuid,nodev,size=4g,mode=1777`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("historical generator lacks hardening flag %q", required)
		}
	}

	generatorStart := strings.Index(script, `if docker run --rm --init --platform linux/amd64 \`)
	if generatorStart < 0 {
		t.Fatal("could not find the hardened historical-generator invocation")
	}
	generatorEnd := strings.Index(script[generatorStart:], `; then`)
	if generatorEnd < 0 {
		t.Fatal("could not find the end of the hardened historical-generator invocation")
	}
	generator := script[generatorStart : generatorStart+generatorEnd]
	for _, required := range []string{
		`--user "$GENERATOR_UID:$GENERATOR_GID"`,
		`--read-only`,
		`--cap-drop ALL`,
		`--security-opt no-new-privileges:true`,
		`--pids-limit 1024`,
		`--memory 8g`,
		`--memory-swap 8g`,
		`--cpus 4`,
		`--tmpfs /tmp:rw,nosuid,nodev,size=4g,mode=1777`,
	} {
		if !strings.Contains(generator, required) {
			t.Errorf("hardened historical generator lacks flag %q", required)
		}
	}
	for _, required := range []string{
		`src=$PROJECT_ROOT,dst=/workspace,readonly`,
		`src=$CONTROL_DIR,dst=/control,readonly`,
		`src=$evidence_dir,dst=/evidence`,
		`src=$cache_dir,dst=/cache`,
		`src=$state_dir,dst=/state`,
	} {
		if !strings.Contains(generator, required) {
			t.Errorf("hardened historical generator lacks mount %q", required)
		}
	}
	if strings.Contains(generator, `src=$OUTPUT_DIR,dst=/output`) {
		t.Error("historical generator must not receive the broad output mount")
	}

	for _, flag := range []string{
		`--init --platform linux/amd64`,
		`--read-only`,
		`--cap-drop ALL`,
		`--security-opt no-new-privileges:true`,
	} {
		if got := strings.Count(script, flag); got != 1 {
			t.Errorf("%s occurrences = %d, want generator-only one", flag, got)
		}
	}
}

func TestMigrationWorkflowUploadsOnlyAllowedCensusArtifactsForEachOutcome(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/migration-test.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)

	for _, forbidden := range []string{
		`${{ runner.temp }}/beads-schema-census-output/census-1-evidence/census.json`,
		`${{ runner.temp }}/beads-schema-census-output/census-2-evidence/census.json`,
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("workflow uploads untrusted generator evidence %q", forbidden)
		}
	}

	for _, test := range []struct {
		step string
		when string
		want map[string]bool
	}{
		{
			step: "Upload sealed census outputs",
			when: "success()",
			want: map[string]bool{
				`${{ runner.temp }}/beads-schema-census-output/runtime-schema-census.json.gz`: true,
				`${{ runner.temp }}/beads-schema-census-output/runtime-schema-routes.json`:    true,
				`${{ runner.temp }}/beads-schema-census-output/runtime-schema-routes.md`:      true,
			},
		},
		{
			step: "Upload validated census failure evidence",
			when: "failure()",
			want: map[string]bool{
				`${{ runner.temp }}/beads-schema-census-output/census-1.json`:            true,
				`${{ runner.temp }}/beads-schema-census-output/census-2.json`:            true,
				`${{ runner.temp }}/beads-schema-census-output/census-1-diagnostic.json`: true,
				`${{ runner.temp }}/beads-schema-census-output/census-2-diagnostic.json`: true,
			},
		},
	} {
		t.Run(test.step, func(t *testing.T) {
			if got := workflowStepScalar(t, workflow, test.step, "if"); got != test.when {
				t.Fatalf("artifact condition = %q, want %q", got, test.when)
			}
			want := test.want
			got := workflowStepMultilineValues(t, workflow, test.step, "path")
			if len(got) != len(want) {
				t.Errorf("uploaded census artifact paths = %d, want %d: %q", len(got), len(want), got)
			}
			for _, path := range got {
				if !want[path] {
					t.Errorf("workflow uploads untrusted or unexpected census artifact %q", path)
				}
				delete(want, path)
			}
			for path := range want {
				t.Errorf("workflow does not upload allowed census artifact %q", path)
			}
		})
	}
	if strings.Contains(workflow, "census-1-evidence/") || strings.Contains(workflow, "census-2-evidence/") {
		t.Error("workflow references raw evidence directories")
	}
	if strings.Contains(workflow, "Upload sealed census outputs\n        if: always()") {
		t.Error("sealed outputs must not upload after a failed census")
	}
	if strings.Contains(workflow, "Upload validated census failure evidence\n        if: always()") {
		t.Error("failure evidence must not upload after a successful census")
	}
	if strings.Contains(workflow, "runtime-schema-census-sealed-${{ github.run_id }}-${{ github.run_attempt }}\n          path: |\n            ${{ runner.temp }}/beads-schema-census-output/census-1.json") {
		t.Error("successful artifact includes duplicate lane JSON")
	}
	if strings.Contains(workflow, "runtime-schema-census-failure-${{ github.run_id }}-${{ github.run_attempt }}\n          path: |\n            ${{ runner.temp }}/beads-schema-census-output/runtime-schema-census.json.gz") {
		t.Error("failure artifact includes sealed outputs")
	}
}

func TestScopedCensusWorkflowsDisablePersistedCheckoutCredentials(t *testing.T) {
	for _, workflowPath := range []string{
		"../../../.github/workflows/migration-test.yml",
		"../../../.github/workflows/schema-census-validation.yml",
	} {
		t.Run(workflowPath, func(t *testing.T) {
			raw, err := os.ReadFile(workflowPath)
			if err != nil {
				t.Fatal(err)
			}
			requireCheckoutCredentialsDisabled(t, string(raw))
		})
	}
}

func workflowStepMultilineValues(t *testing.T, workflow, stepName, key string) []string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	stepStart := -1
	stepIndent := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "- name: "+stepName {
			stepStart = i
			stepIndent = leadingSpaces(line)
			break
		}
	}
	if stepStart < 0 {
		t.Fatalf("workflow lacks step %q", stepName)
	}

	stepEnd := workflowStepEnd(lines, stepStart, stepIndent)
	for i := stepStart + 1; i < stepEnd; i++ {
		if strings.TrimSpace(lines[i]) != key+": |" {
			continue
		}
		keyIndent := leadingSpaces(lines[i])
		var values []string
		for j := i + 1; j < stepEnd; j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if leadingSpaces(lines[j]) <= keyIndent {
				break
			}
			values = append(values, strings.TrimSpace(lines[j]))
		}
		return values
	}
	t.Fatalf("workflow step %q lacks multiline %s input", stepName, key)
	return nil
}

func workflowStepScalar(t *testing.T, workflow, stepName, key string) string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	stepStart := -1
	stepIndent := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "- name: "+stepName {
			stepStart = i
			stepIndent = leadingSpaces(line)
			break
		}
	}
	if stepStart < 0 {
		t.Fatalf("workflow lacks step %q", stepName)
	}
	for _, line := range lines[stepStart+1 : workflowStepEnd(lines, stepStart, stepIndent)] {
		if strings.TrimSpace(line) == key+": success()" {
			return "success()"
		}
		if strings.TrimSpace(line) == key+": failure()" {
			return "failure()"
		}
	}
	t.Fatalf("workflow step %q lacks scalar %s", stepName, key)
	return ""
}

func requireCheckoutCredentialsDisabled(t *testing.T, workflow string) {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	checkouts := 0
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "- uses: actions/checkout@") {
			continue
		}
		checkouts++
		stepIndent := leadingSpaces(line)
		stepEnd := workflowStepEnd(lines, i, stepIndent)
		disabled := false
		for j := i + 1; j < stepEnd; j++ {
			if strings.TrimSpace(lines[j]) != "with:" {
				continue
			}
			withIndent := leadingSpaces(lines[j])
			for k := j + 1; k < stepEnd; k++ {
				if strings.TrimSpace(lines[k]) == "" {
					continue
				}
				if leadingSpaces(lines[k]) <= withIndent {
					break
				}
				if strings.TrimSpace(lines[k]) == "persist-credentials: false" {
					disabled = true
				}
			}
		}
		if !disabled {
			t.Errorf("checkout at workflow line %d persists credentials", i+1)
		}
	}
	if checkouts == 0 {
		t.Error("workflow has no checkout steps")
	}
}

func workflowStepEnd(lines []string, start, stepIndent int) int {
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		indent := leadingSpaces(lines[i])
		if indent < stepIndent || indent == stepIndent && strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") {
			return i
		}
	}
	return len(lines)
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
