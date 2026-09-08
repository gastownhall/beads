# Release gate: be-ow4w54 — Extract markdown rendering out of internal/ui

**Verdict: PASS for the internal/ui decoupling scope.**

Scope note: this gate verifies that `internal/ui` no longer imports the
glamour/chroma markdown renderer stack and that direct markdown-rendering
callers use `internal/uimd`. It does not prove that every `bd` subprocess avoids
glamour/chroma startup work; `cmd/bd` is a single Go binary, so that broader
runtime claim needs a separate startup/dependency boundary and measurement.

Branch: `feat/be-ow4w54-uimd-markdown`
Base on origin: `origin/main` (merge-base `74c66722b14cc32e70ecc4182788d4de40b6d906`)
Reviewed contributor head: `228ead67e34d2ce77254d65a5c3bdb98b7854bbf`
Source bead: `be-ow4w54`; review bead: `be-2y5h4f` (PASS).

## Commits

| # | SHA | Subject |
|---|-----|---------|
| 1 | `228ead67e` | refactor(ui): extract markdown rendering to internal/uimd package |
| 2 | local maintainer fixup | docs: narrow uimd release gate scope |

Original contributor change: `cmd/bd/comments.go`, `cmd/bd/show.go`,
`cmd/bd/show_children.go`, `cmd/bd/show_display.go`,
`internal/{ui→uimd}/markdown.go`.

Maintainer fixup: narrows this gate to the delivered `internal/ui`
decoupling scope and restores rationale comments in
`internal/uimd/markdown.go`.

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-2y5h4f notes: "VERDICT: pass. Findings: none." |
| 2 | Acceptance criteria met | **PASS** | `internal/ui` contains no glamour/chroma imports (`grep -rn 'glamour\|chroma' internal/ui/` -> empty). `internal/uimd/markdown.go` created and all 4 direct markdown callers (show.go, show_display.go, show_children.go, comments.go) updated to import `internal/uimd`. Build clean. |
| 3 | `internal/ui` dependency graph stays renderer-free | **PASS** | `go list -deps ./internal/ui \| grep -Ec 'charm\.land/glamour\|alecthomas/chroma'` -> `0`. |
| 4 | Tests pass | **PASS** | `go test -tags gms_pure_go -count=1 -short ./internal/ui/... ./internal/uimd/...`: ok (0.005s). `go test -tags gms_pure_go -count=1 -short -run TestShow ./cmd/bd/`: ok (0.335s). `go build -tags gms_pure_go ./...`: clean. Note: `TestCheckBeadsRole_*` failures in `cmd/bd/doctor` are pre-existing rig-env failures unrelated to this change. |
| 5 | No high-severity review findings open | **PASS** | Reviewer be-2y5h4f: "Findings: none." |
| 6 | Final branch is clean | **PASS** | `git status` clean (untracked `.gc/`, `.gitkeep` are rig scaffolding). |
| 7 | Branch diverges cleanly from main | **PASS** | Contributor head rebased cleanly onto `origin/main`; maintainer fixup remains local for the next review pass. |

## Test environment

- Host: Linux 6.19.14-300.fc44.x86_64

## Push target

`PUSH_REMOTE=fork`. Cross-repo PR head: `quad341:feat/be-ow4w54-uimd-markdown`.
