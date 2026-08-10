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
#   E. A new main-plane migration must not PREPARE/EXECUTE a DML statement
#      (UPDATE/INSERT/DELETE built into a `SET @sql = '...'` string). The
#      Dolt CLI batch path (`dolt sql -q`/`-f`, the AllMigrationsSQL()/
#      fresh-bundle route) silently no-ops a prepared write there while
#      EXECUTE reports success (dolthub/dolt#11345, mybd-p8i3). Prepared
#      ALTER TABLE is the same underlying limitation but a separate, accepted
#      idiom for idempotent DDL re-runs (see cli_migrations.go), so it is not
#      flagged. Neither is a prepared `INSERT INTO __<standin>`: that IS the
#      recommended pattern (0059, gastownhall/beads#4877), where a silent
#      no-op degrades gracefully instead of corrupting state. The exemption
#      stops there on purpose -- a prepared UPDATE or DELETE is never
#      exempted, because their multi-table forms put the modified table after
#      a JOIN (`UPDATE __stage s JOIN issues i ... SET i.priority = 0` writes
#      to issues) and the real target cannot be identified reliably by
#      pattern-matching. INSERT INTO's target always is. The write verb is
#      matched as a token ANYWHERE in the prepared text, not just at its
#      start, since '/* c */ UPDATE ...' and 'WITH t AS (...) UPDATE ...' are
#      both valid; ON UPDATE / ON DUPLICATE KEY UPDATE are masked first so a
#      column attribute is not read as a write. Both `=` and `:=` assignment
#      forms are recognised. Only .up.sql is scanned, since only
#      migrations/*.up.sql is embedded into the bundle.
#
#      KNOWN LIMITS — this is a best-effort heuristic, not a SQL parser, and
#      says so rather than implying coverage it does not have. Six rounds of
#      cross-vendor review each found another shape the previous fix missed;
#      the pattern of the misses (position-anchored matching) was addressed,
#      but the correlation between a `SET @var` and its later
#      `PREPARE ... FROM @var` is still line-oriented, so these bypass it.
#      `PREPARE ... FROM '<literal>'`, with no variable at all, IS covered,
#      so do not read this list as broader than it is:
#        - multi-variable assignment: `SET @guard = 1, @sql = 'UPDATE ...';`
#          (only the first variable is tracked)
#        - a PREPARE split across lines: `PREPARE stmt` / `FROM @sql;`
#        - a literal `;` inside the prepared string (ends buffering early)
#      None of these forms occurs anywhere in the current migration tree, and
#      the check is advisory hygiene on new files that a human reviews anyway,
#      so the gap is a follow-up (mybd-q8hy2) rather than a
#      reason to withhold the guard. A reviewer who sees any of these three
#      shapes in a new migration should not trust a green check here.
#      This is a going-forward guard, and deliberately new-files-only: seven
#      shipped main-plane migrations (0035, 0037, 0041, 0047, 0053, 0055,
#      0058) already PREPARE writes to real tables. They are not a live bug
#      and are not retrofits waiting to happen — a shipped migration is
#      frozen, and on the one path this check is about (the fresh-schema
#      bundle, i.e. an empty database) a data backfill is a no-op whether or
#      not the prepared write lands. cliCompatibleMigrationSQL overrides only
#      the subset whose prepared statements change the committed schema
#      SHAPE, which is what the bundle's contract actually promises. The
#      point of the check is that the idiom stops spreading into migrations
#      where the write does matter.
#
# Checks C, D, and E compare against $BASE_SHA if set (CI passes the PR base),
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
  ignored_tables='wisps|wisp_[a-z_]+|repo_mtimes|local_metadata|leases|events|bd_events_journal|bd_events_seq|ignored_schema_migrations'
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

# --- Check E: no PREPARE'd DML in new main-plane migrations -----------------
# AllMigrationsSQL() (schema.go) only walks the main-plane migration series
# to build the CLI fresh-bundle route, so this check is scoped to added_main
# the same as check D; migrations/ignored/ never goes through `dolt sql -q/-f`
# and is not scanned here.
#
# A PREPARE'd write is: `SET @var = '...'` (or `SET @var = IF(cond, '...',
# '...')`) followed by `PREPARE <name> FROM @var; EXECUTE <name>`, where the
# quoted text's first keyword is insert/update/delete. Statements are
# correlated by variable name and buffered across lines so a multi-line
# `SET @sql = IF(...)` (the common form here) is read as one statement; each
# statement is expected to be self-contained between `=` and its terminating
# `;` (true for every migration in this tree — none embed a literal `;`
# inside the prepared-text string). Prepared ALTER TABLE uses the identical
# @sql/PREPARE stmt/EXECUTE stmt shape and is deliberately not flagged: only
# the leading keyword inside the quoted text decides.
# prepared_target_is_standin STMT — true when the prepared DML writes to a
# migration-local stand-in table rather than a real one. That is the pattern
# this check RECOMMENDS (0059, gastownhall/beads#4877): copy into a throwaway
# table with PREPARE, then have a direct statement read it, so a silent no-op
# degrades gracefully instead of corrupting state. Flagging it would reject the
# very remedy the failure message hands out.
#
# The convention in this tree is a double-underscore prefix — __bd_0059_* and
# __temp__* are the existing instances — and a real beads table never starts
# with one. STMT is already lowercased by the caller.
# neutralize_sql_clauses TEXT — mask the UPDATE keywords that are column/insert
# clauses rather than write verbs, so the scans below cannot mistake them for
# one. `ON UPDATE CURRENT_TIMESTAMP` is a column attribute; `ON DUPLICATE KEY
# UPDATE` belongs to an INSERT.
neutralize_sql_clauses() {
  local t="$1"
  t=${t//on duplicate key update/on duplicate key __updclause}
  t=${t//on update/on __updclause}
  printf '%s' "$t"
}

# prepared_has_dml STMT — does the statement carry a write verb ANYWHERE?
#
# Deliberately not "does the quoted text START with one". A prepared string may
# legitimately open with a comment or a CTE —
#   '/* conditional */ UPDATE issues ...'      'WITH t AS (...) UPDATE issues ...'
# — and a position-anchored test waves both through, which is the same mistake
# as trying to locate a write target by position. Matching the verb as a whole
# token anywhere is strictly more conservative: the cost is over-flagging a
# statement that merely mentions one, and the exemption below is what rescues
# the legitimate stand-in forms.
prepared_has_dml() {
  local scan
  scan="$(neutralize_sql_clauses "$1")"
  printf '%s' "$scan" | grep -qE "(^|[^a-z0-9_])(insert|update|delete|replace)([^a-z0-9_]|$)"
}

prepared_target_is_standin() {
  local stmt="$1" scan targets t
  scan=$(printf '%s' "$stmt")

  scan="$(neutralize_sql_clauses "$scan")"

  # UPDATE, DELETE and REPLACE are never exempted, even when the first table after the
  # verb is a stand-in. Their multi-table forms put the modified table after a
  # JOIN — `UPDATE __stage s JOIN issues i ... SET i.priority = 0` writes to
  # issues, not to __stage — so the write target cannot be identified reliably
  # by pattern-matching. Three review rounds were spent trying; the honest
  # conclusion is that the exemption belongs only on a form whose target is
  # unambiguous. An author who genuinely needs a conditional UPDATE should
  # restructure it the way 0059 does rather than have this check guess.
  # Boundary matches prepared_has_dml's (any non-word char, not whitespace):
  # Dolt takes a comment as a token separator, so `UPDATE/*x*/ issues ...` is a
  # real write and a whitespace-anchored test walked straight past it.
  if printf '%s' "$scan" | grep -qE "(^|[^a-z0-9_])(update|delete|replace)([^a-z0-9_]|$)"; then
    return 1
  fi

  # INSERT INTO <tbl> is unambiguous: the target is always the token after
  # INTO, whatever the SELECT source does. Every one of them must be a
  # stand-in. (A mixed `SET @sql = IF(cond, '<stand-in>', '<real>')` carries
  # two branches and only one executes, so "the first match was a stand-in" is
  # not good enough.)
  targets=$(
    printf '%s\n' "$scan" \
      | grep -oE "insert[[:space:]]+(ignore[[:space:]]+)?into[[:space:]]+\`?[a-z0-9_]+" \
      | grep -oE "[a-z0-9_]+$"
  ) || true
  [ -n "$targets" ] || return 1

  # Every INSERT must be one we could classify. MySQL/Dolt accept `INSERT
  # issues ...` with INTO omitted, so a conditional carrying
  # `IF(c, 'INSERT INTO __stage ...', 'INSERT issues ...')` would otherwise
  # yield one stand-in target, look unanimous, and exempt the real-table write
  # in the other branch. Count the verbs and refuse to exempt unless every one
  # of them produced a target.
  local n_verbs n_targets
  n_verbs=$(printf '%s\n' "$scan" | grep -oE "(^|[^a-z0-9_])insert([^a-z0-9_])" | wc -l)
  n_targets=$(printf '%s\n' "$targets" | grep -c .)
  [ "$n_verbs" -eq "$n_targets" ] || return 1
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    case "$t" in
      __*) ;;
      *) return 1 ;;
    esac
  done <<EOF
$targets
EOF
  return 0
}

if [ -z "$base" ] || [ -z "${merge_base:-}" ]; then
  echo "WARN (prepared DML) no usable base ref; skipping check E." >&2
else
  # Only *.up.sql is embedded into the CLI fresh bundle (`//go:embed
  # migrations/*.up.sql`, schema.go:218), so a .down.sql can never meet the bug
  # this check is about. Scanning it would reject valid work for no reason.
  for f in $(grep '\.up\.sql$' <<<"$added_main" || true); do
    [ -f "$f" ] || continue
    stripped=$(sed 's/--.*$//' "$f" | tr '[:upper:]' '[:lower:]')
    declare -A dml_flagged=()
    dml_var=""
    stmt_buf=""
    lineno=0
    hits=""
    while IFS= read -r line; do
      lineno=$((lineno + 1))
      if [ -n "$dml_var" ]; then
        stmt_buf="$stmt_buf $line"
      elif [[ "$line" =~ set[[:space:]]+@([a-z0-9_]+)[[:space:]]*:?= ]]; then
        dml_var="${BASH_REMATCH[1]}"
        stmt_buf="$line"
      fi
      if [ -n "$dml_var" ] && [[ "$stmt_buf" == *";"* ]]; then
        if prepared_has_dml "$stmt_buf" && ! prepared_target_is_standin "$stmt_buf"; then
          dml_flagged["$dml_var"]=1
        else
          unset "dml_flagged[$dml_var]"
        fi
        dml_var=""
        stmt_buf=""
      fi
      if [[ "$line" =~ prepare[[:space:]]+[a-z0-9_]+[[:space:]]+from[[:space:]]+@([a-z0-9_]+) ]]; then
        pvar="${BASH_REMATCH[1]}"
        if [ "${dml_flagged[$pvar]:-0}" = "1" ]; then
          hits="${hits}${lineno}: ${line}"$'\n'
        fi
      elif [[ "$line" =~ prepare[[:space:]]+[a-z0-9_]+[[:space:]]+from[[:space:]]+[\'\"] ]]; then
        # `PREPARE stmt FROM 'UPDATE issues ...'` — the literal form, with no
        # user variable in play at all. This is the most direct spelling of the
        # hazard, not an exotic one, so it is checked in place rather than
        # through the assignment bookkeeping above.
        if prepared_has_dml "$line" && ! prepared_target_is_standin "$line"; then
          hits="${hits}${lineno}: ${line}"$'\n'
        fi
      fi
    done <<<"$stripped"
    unset dml_flagged
    if [ -n "$hits" ]; then
      fail=1
      echo "FAIL (prepared DML) $f contains a PREPARE'd INSERT/UPDATE/DELETE:"
      echo "$hits" | sed '/^$/d; s/^/  line /'
      cat <<EOF
  A PREPARE'd UPDATE/INSERT/DELETE silently no-ops under the Dolt CLI batch
  path used to build the fresh-schema bundle (dolthub/dolt#11345): EXECUTE
  reports success and changes nothing. Write the real-table mutation as
  direct SQL; if it needs to be conditional, gate it on a stand-in table that
  the direct statement reads instead of PREPARE'ing the write itself
  (precedent: 0059, gastownhall/beads#4877 -- a prepared INSERT INTO a
  __-prefixed stand-in is recognised and not flagged; a prepared UPDATE or
  DELETE is never exempted, since a JOIN can move its real target out of
  reach of this check). Prepared ALTER TABLE is a separate, accepted idiom
  and is not what this check flags either.
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
