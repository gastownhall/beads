package workflowtags

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRun(t *testing.T) {
	tests := []struct {
		name       string
		run        string
		violations int
	}{
		{name: "direct tagged", run: `go test -tags=gms_pure_go ./...`},
		{name: "assignment prefix", run: `CGO_ENABLED=0 go build -tags "netgo,gms_pure_go" ./cmd/bd`},
		{name: "env prefix", run: `env CGO_ENABLED=0 go test '-tags=integration,gms_pure_go' ./...`},
		{name: "env unset", run: `env -u GOFLAGS go test ./...`, violations: 1},
		{name: "env long unset operand", run: `env --unset GOFLAGS go test ./...`, violations: 1},
		{name: "env long unset", run: `env --unset=GOFLAGS go test ./...`, violations: 1},
		{name: "env chdir", run: `env -C /tmp go build ./cmd/bd`, violations: 1},
		{name: "env long chdir", run: `env --chdir /tmp go build ./cmd/bd`, violations: 1},
		{name: "env ignore environment tagged", run: `env -i go test -tags=gms_pure_go ./...`},
		{name: "env double dash", run: `env -- go test ./...`, violations: 1},
		{name: "timed command", run: `ci_time "label mentions go test" -- go test -tags gms_pure_go ./...`},
		{name: "quoted hash", run: `go test -tags=gms_pure_go -run 'Test#Case' ./...`},
		{name: "continuation", run: "go test \\\n  -tags=gms_pure_go \\\n  ./..."},
		{name: "pinned install", run: `go install example.com/tool@v1.2.3`},
		{name: "multiple pinned installs", run: `go install example.com/tool@v1.2.3 example.com/other@latest`},
		{name: "pinned run", run: `go run example.com/tool@latest`},
		{name: "pinned run arguments", run: `go run example.com/tool@latest --help`},
		{name: "pinned tool with flags is outside exemption", run: `go install -pgo off example.com/tool@v1.2.3`, violations: 1},
		{name: "bare", run: `go test ./...`, violations: 1},
		{name: "source is not workflow authority", run: "source ./.buildflags\ngo test ./...", violations: 1},
		{name: "variable tag", run: `go build -tags "$BEADS_BUILD_TAGS" ./cmd/bd`, violations: 1},
		{name: "tag substring prefix", run: `go test -tags=notgms_pure_go ./...`, violations: 1},
		{name: "tag substring suffix", run: `go test -tags=gms_pure_go_extra ./...`, violations: 1},
		{name: "last tag wins negative", run: `go test -tags=gms_pure_go -tags=integration ./...`, violations: 1},
		{name: "last tag wins positive", run: `go test -tags=integration -tags=gms_pure_go ./...`},
		{name: "tag after separator", run: `go test ./... -- -tags=gms_pure_go`, violations: 1},
		{name: "tag after test args", run: `go test ./... -args -tags=gms_pure_go`, violations: 1},
		{name: "missing tag value", run: `go test -tags ./...`, violations: 1},
		{name: "unrelated version text", run: `go run ./cmd/bd --label @v1`, violations: 1},
		{name: "unquoted hash is argument text", run: `go test -run Test#Case -tags=gms_pure_go ./...`},
		{name: "compound suffix outside bounded command", run: `go test -tags=gms_pure_go ./... && go test ./...`},
		{name: "compound suffix cannot authorize first command", run: `go test ./... && go test -tags=gms_pure_go ./...`, violations: 1},
		{name: "dynamic shell excluded", run: `bash -c 'go test ./...'`},
		{name: "eval excluded", run: `eval 'go test ./...'`},
		{name: "substitution excluded", run: `out="$(go test ./...)"`},
		{name: "wrapper excluded", run: `gotestsum -- -tags=gms_pure_go ./...`},
		{name: "comment ignored", run: `# explain go test behavior`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckRun(tt.run)
			if len(got) != tt.violations {
				t.Fatalf("got %d violation(s), want %d: %#v", len(got), tt.violations, got)
			}
		})
	}
}

func TestCheckDirParsesOnlyRunScalars(t *testing.T) {
	dir := t.TempDir()
	fixture := `name: Explain go test behavior
jobs:
  check:
    steps:
      - name: Inline
        run: go test -tags=gms_pure_go ./...
      - name: Literal
        run: |
          go build -tags gms_pure_go ./cmd/bd
      - name: Folded
        run: >-
          go test
          -tags=gms_pure_go
          ./...
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.yml"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	violations, err := CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("unexpected violations: %#v", violations)
	}
}

func TestCheckDirRejectsCrossStepSourceAuthority(t *testing.T) {
	dir := t.TempDir()
	fixture := `jobs:
  check:
    steps:
      - name: Source flags
        run: source ./.buildflags
      - name: Bare command
        run: go test ./...
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.yml"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	violations, err := CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Step != "Bare command" {
		t.Fatalf("violations = %#v, want one in Bare command", violations)
	}
}

func TestCheckDirRejectsEmptyInventory(t *testing.T) {
	if _, err := CheckDir(t.TempDir()); err == nil {
		t.Fatal("CheckDir succeeded without workflow files")
	}
}
