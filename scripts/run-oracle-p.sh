#!/usr/bin/env bash
#
# Deep conformance gate: the bts-rs 523-scenario differential oracle, PG-vs-Dolt.
# Reference = bd in Dolt mode; candidate = the SAME bd via a thin flag-injector that
# runs native `bd init --backend=postgres`. On-demand only (needs the bts-rs checkout,
# a live Postgres, and ~50 min); invoked by scripts/conformance.sh when CONFORMANCE_DEEP=1.
#
# Env:
#   BTS_RS_DIR         path to the bts-rs checkout                 (required)
#   BEADS_PG_TEST_URL  postgres URL incl. password                 (required)
#   BD                 path to a prebuilt bd binary                (optional; built if unset)
#
set -euo pipefail
cd "$(dirname "$0")/.."
: "${BTS_RS_DIR:?set BTS_RS_DIR to the bts-rs checkout}"
: "${BEADS_PG_TEST_URL:?set BEADS_PG_TEST_URL (postgres URL incl. password)}"

BD="${BD:-}"
if [[ -z "$BD" ]]; then
  BD="$(mktemp -d)/bd"
  echo "### building bd -> $BD"
  CGO_ENABLED=1 go build -tags gms_pure_go -o "$BD" ./cmd/bd
fi

# Thin flag-injector: native `bd init` does the metadata + schema + seeding; this only
# supplies the backend flags the backend-agnostic harness cannot know, plus a
# deterministic per-workspace schema and a default prefix so IDs match the reference.
WRAP="$(mktemp)"
cat > "$WRAP" <<EOF
#!/usr/bin/env bash
export BEADS_POSTGRES_URL="$BEADS_PG_TEST_URL"
if [ "\$1" = "init" ]; then
  schema="w\$(printf '%s' "\$PWD" | md5sum | cut -c1-16)"
  has_p=0; for a in "\$@"; do [ "\$a" = "-p" ] && has_p=1; done
  extra=(--backend=postgres --pg-url="$BEADS_PG_TEST_URL" --pg-schema="\$schema")
  [ "\$has_p" = "0" ] && extra+=(-p wp)
  exec "$BD" "\$@" "\${extra[@]}"
fi
exec "$BD" "\$@"
EOF
chmod +x "$WRAP"
trap 'rm -f "$WRAP"' EXIT

echo "### clean slate: dropping accumulated w% schemas"
psql "$BEADS_PG_TEST_URL" -tAc \
  "select 'drop schema \"'||schema_name||'\" cascade;' from information_schema.schemata where schema_name like 'w%';" \
  2>/dev/null | psql "$BEADS_PG_TEST_URL" >/dev/null 2>&1 || true

cd "$BTS_RS_DIR"
echo "### capturing goldens from bd-Dolt"
BTS_CATALOG=1 BTS_REFERENCE_BD="$BD" cargo run --release -q -p bts-conformance --bin capture_golden 2>&1 | tail -4
echo "### scoring native bd-PG against fresh goldens"
BTS_CATALOG=1 BTS_CANDIDATE="$WRAP" BTS_DATABASE_URL="$BEADS_PG_TEST_URL" \
  cargo run --release -q -p bts-conformance --bin scoreboard 2>&1 | tail -30
git -C "$BTS_RS_DIR" checkout crates/bts-conformance/testdata/golden >/dev/null 2>&1 || true
echo "### DONE"
