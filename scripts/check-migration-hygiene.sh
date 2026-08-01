#!/usr/bin/env bash
# check-migration-hygiene.sh — source-time guards for schema migrations.
#
# Three failure classes from the 2026-04..06 window (audit epic bd-6dnrw),
# all enabled by version-number-only migration tracking:
#
#   1. Duplicate version numbers (PR 4027 merged a second 0046): the loser
#      silently never applies on databases already at that version.
#   2. Nondeterministic SQL (PR 4039's DEFAULT (UUID()) on dependencies.id,
#      root cause of the #4259 merge corruption): per-clone-random values in
#      replicated tables make every clone diverge by construction.
#   3. In-place edits to shipped migrations (PRs 3991/3918/3942 rewrote
#      0021..0035 after release): fresh clones and upgraded clones end up at
#      the same schema_migrations version with different actual schemas.
#
# Checks, correspondingly:
#   A. No duplicate migration version numbers (per directory).
#   B. No UUID()/UUID_SHORT()/NOW()/RAND() — nor CURRENT_TIMESTAMP outside a
#      DEFAULT / ON UPDATE column attribute — in migration SQL unless the file
#      is listed in migrations/nondeterminism-allowlist.txt with a
#      justification.
#   C. No modification/deletion/rename of a migration file that already
#      exists on the base branch. New files only. Fix-forward with a new
#      migration instead: applied migration content is content-hashed
#      (PR 4270), so editing history creates cross-clone hash skew.
#   D. A new main-plane migration whose DDL touches a clone-local
#      (dolt_ignored) table must ship an ignored-series twin in the same
#      change. Clone-local tables are materialized on fresh clones by the
#      ignored series alone — the main cursor arrives at-latest, so a
#      main-plane ALTER silently never reaches them (bd-hs7fa: 0060's
#      wisps.storage_class missed every fresh clone; wy-pt82l before it:
#      0054's wisps.row_lock, healed after the fact by ignored/0013).
#
# Checks C and D compare against $BASE_SHA if set (CI passes the PR base),
# else origin/main, else main; they are skipped with a warning when no base
# is resolvable (e.g. shallow clone without the base commit).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

MIG_DIR="internal/storage/schema/migrations"
ALLOWLIST="$MIG_DIR/nondeterminism-allowlist.txt"
fail=0

# --- Check A: duplicate version numbers ------------------------------------
# Numbering is per directory: migrations/ and migrations/ignored/ are
# independent sequences (see ignored/ for local-state tables).
for dir in "$MIG_DIR" "$MIG_DIR/ignored"; do
  [ -d "$dir" ] || continue
  dups=$(
    find "$dir" -maxdepth 1 -name '*.up.sql' \
      | sed 's|.*/||; s/^\([0-9][0-9]*\)_.*/\1/' \
      | sort | uniq -d
  )
  if [ -n "$dups" ]; then
    fail=1
    echo "FAIL (duplicate versions) in $dir:"
    while IFS= read -r v; do
      find "$dir" -maxdepth 1 -name "${v}_*.up.sql" -print | sed 's/^/  /'
    done <<< "$dups"
    echo "  Renumber the newer file to the next free version."
  fi
done

# --- Check B: nondeterministic SQL ------------------------------------------
# UUID(), UUID_SHORT(), NOW(), RAND() evaluate differently per clone / per
# run. In a replicated migration that means divergent data under identical
# versions. CURRENT_TIMESTAMP is the same hazard when it executes at
# migration time (UPDATE ... SET x = CURRENT_TIMESTAMP), but as a column
# attribute (DEFAULT ... / ON UPDATE ...) it evaluates at query time on every
# clone and is fine — those positions are excluded.
# Matches inside single-quoted dynamic SQL strings are intentional positives
# (0037/0043 execute exactly that way); '-- ' comments are stripped first.
# The allowlist grandfathers shipped files; additions require a justification
# on the same line and land under CODEOWNERS review.
allowed() {
  local rel="$1"
  [ -f "$ALLOWLIST" ] || return 1
  grep -E -q "^${rel}[[:space:]]" "$ALLOWLIST"
}

while IFS= read -r f; do
  rel="${f#"$MIG_DIR"/}"
  [ "$rel" = "nondeterminism-allowlist.txt" ] && continue
  stripped=$(sed 's/--.*$//' "$f" | tr '[:upper:]' '[:lower:]')
  hits=$(
    {
      grep -n -E '(^|[^a-z0-9_])(uuid|uuid_short|now|rand)[[:space:]]*\(' <<<"$stripped" || true
      sed -E 's/(default|on +update) +current_timestamp(\(\))?//g' <<<"$stripped" \
        | grep -n -E '(^|[^a-z0-9_])current_timestamp' || true
    } | sort -t: -k1,1n -u
  )
  if [ -n "$hits" ]; then
    if allowed "$rel"; then
      continue
    fi
    fail=1
    echo "FAIL (nondeterministic SQL) $f:"
    echo "$hits" | sed 's/^/  line /'
    cat <<EOF
  UUID()/UUID_SHORT()/NOW()/RAND()/migration-time CURRENT_TIMESTAMP in a
  migration produces per-clone-divergent results (see #4259). Compute values
  in application code, or if this use is truly safe (e.g. query-time
  evaluation inside a VIEW body), add a line to $ALLOWLIST:
    $rel  <why this is deterministic-equivalent across clones>
EOF
  fi
done < <(find "$MIG_DIR" -name '*.sql' | sort)

# Allowlist hygiene: every entry must still reference an existing file and
# carry a justification.
if [ -f "$ALLOWLIST" ]; then
  while IFS= read -r line; do
    case "$line" in ''|'#'*) continue ;; esac
    entry="${line%%[[:space:]]*}"
    just="${line#"$entry"}"
    if [ ! -f "$MIG_DIR/$entry" ]; then
      fail=1
      echo "FAIL (allowlist) stale entry, file does not exist: $entry"
    fi
    if [ -z "$(echo "$just" | tr -d '[:space:]')" ]; then
      fail=1
      echo "FAIL (allowlist) entry missing justification: $entry"
    fi
  done < "$ALLOWLIST"
fi

# --- Check C: shipped migrations are frozen ----------------------------------
base="${BASE_SHA:-}"
if [ -z "$base" ]; then
  for candidate in origin/main main; do
    if git rev-parse --verify -q "$candidate^{commit}" >/dev/null 2>&1; then
      base="$candidate"
      break
    fi
  done
fi

if [ -z "$base" ] || ! merge_base=$(git merge-base "$base" HEAD 2>/dev/null); then
  echo "WARN (frozen migrations) no usable base ref; skipping check C." >&2
else
  # Diff merge-base against the working tree (no HEAD) so local uncommitted
  # edits are caught too; in CI the tree is clean so this equals the PR diff.
  frozen=$(git diff --name-status --diff-filter=MDR "$merge_base" -- \
    "$MIG_DIR/*.sql" "$MIG_DIR/ignored/*.sql" || true)
  if [ -n "$frozen" ]; then
    fail=1
    echo "FAIL (frozen migrations) files on the base branch were changed:"
    echo "$frozen" | sed 's/^/  /'
    cat <<'EOF'
  Migration files that exist on main are frozen: clones have already applied
  them and their content hashes are recorded (schema_migrations.content_hash).
  Editing, deleting, or renaming one forks fresh clones from upgraded clones.
  Write a NEW migration with the next version number instead.
EOF
  fi
fi

# --- Check D: main-plane DDL on clone-local tables needs an ignored twin -----
# Fresh clones materialize the clone-local (dolt_ignored) tables from the
# ignored series with the MAIN cursor already at-latest, so a main-plane
# migration's DDL against one of them never executes there. The main-plane
# migration is still required (it upgrades in-place workspaces, whose ignored
# cursor is likewise at-latest); the twin is what carries the change through
# the fresh-clone door. Data-only statements (UPDATE/INSERT/DELETE) don't
# trigger this: clone-local data is clone-local by design.
#
# The table list mirrors internal/storage/schema/schema.go's
# doltIgnorePatterns + versionGatedDoltIgnorePatterns. The engine-level
# backstop for the same invariant is
# internal/storage/embeddeddolt/migrate_ignored_plane_shape_test.go.
if [ -z "$base" ] || [ -z "${merge_base:-}" ]; then
  echo "WARN (ignored twins) no usable base ref; skipping check D." >&2
else
  ignored_tables='wisps|wisp_[a-z_]+|repo_mtimes|local_metadata|leases|events|ignored_schema_migrations'
  ddl_re="(alter|create|rename|drop)[[:space:]]+table([[:space:]]+if[[:space:]]+(not[[:space:]]+)?exists)?[[:space:]]+[\`']?(${ignored_tables})\b"
  # Untracked files count as added too (mirrors check C's working-tree scope:
  # in CI the tree is clean so this equals the PR diff).
  added_all=$(
    {
      git diff --name-only --diff-filter=A "$merge_base" -- "$MIG_DIR/*.sql" "$MIG_DIR/ignored/*.sql"
      git ls-files --others --exclude-standard -- "$MIG_DIR"
    } | grep '\.sql$' | sort -u || true
  )
  added_main=$(grep -v '/ignored/' <<<"$added_all" || true)
  added_ignored=$(grep '/ignored/' <<<"$added_all" || true)
  for f in $added_main; do
    [ -f "$f" ] || continue
    stripped=$(sed 's/--.*$//' "$f" | tr '[:upper:]' '[:lower:]')
    hits=$(grep -n -E "$ddl_re" <<<"$stripped" || true)
    if [ -n "$hits" ] && [ -z "$added_ignored" ]; then
      fail=1
      echo "FAIL (ignored twin missing) $f contains DDL against a clone-local table:"
      echo "$hits" | sed 's/^/  line /'
      cat <<'EOF'
  This table is dolt_ignored (clone-local): fresh clones build it from the
  ignored migration series with the main cursor already at-latest, so this
  DDL will silently never run there (bd-hs7fa). Ship a guarded twin under
  internal/storage/schema/migrations/ignored/ in the same PR (precedent:
  ignored/0013 for main 0054, ignored/0020 for main 0060). If the change is
  genuinely main-plane-only (e.g. the table is being flipped ONTO the
  ignored plane by this very migration), the twin is the migration that
  materializes the table for clones (precedent: 0062 + ignored/0019).
EOF
    fi
  done
fi

if [ "$fail" -ne 0 ]; then
  echo
  echo "Migration hygiene check failed. See docs in scripts/check-migration-hygiene.sh."
  exit 1
fi
echo "Migration hygiene OK."
