package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ddl is the Postgres backend schema, embedded verbatim from
// internal/storage/postgres/schema.sql. It is applied statement-by-statement by
// InitSchema because the pgx extended protocol forbids multiple statements per
// Exec.
const ddl = `-- bd Postgres backend schema.
-- Machine-translated from Dolt ` + "`" + `SHOW CREATE TABLE` + "`" + ` output at HEAD (all
-- migrations applied), using the conformance type map documented in
-- docs/architecture/storage-backends.md:
--   varchar/char/text/longtext -> text   (see collation note below)
--   datetime                   -> timestamp(0)       (naive whole-second UTC)
--   tinyint(1)                 -> smallint
--   json DEFAULT (json_object()) -> jsonb DEFAULT '{}'
--   int -> integer, bigint -> bigint
--   ON UPDATE CURRENT_TIMESTAMP -> (no trigger; the shared layer always assigns
--                                   updated_at explicitly on every UPDATE)
--   inline KEY / UNIQUE KEY     -> CREATE [UNIQUE] INDEX after each table
-- All statements are idempotent (IF NOT EXISTS / OR REPLACE); run with
-- search_path pinned to the per-workspace schema.
-- Views ready_issues / blocked_issues intentionally omitted (unused).
--
-- Collation note: Dolt's text columns are utf8mb4_0900_bin (byte / code-point
-- order). Per-column COLLATE "C" was dropped from this DDL because it breaks the
-- shared layer's parameter-seeded recursive CTEs (ERROR: recursive query column
-- has collation "default" in non-recursive term but collation "C" overall). Text
-- columns therefore use the database's default collation, so byte-order parity
-- with Dolt is a documented wedge gap that must be guaranteed at DEPLOYMENT time
-- by creating the database with LC_COLLATE 'C' (CREATE DATABASE ... TEMPLATE
-- template0 LC_COLLATE 'C' LC_CTYPE 'C'), NOT per-column. Under any glibc/ICU
-- en_US.* collation, every text ORDER BY the shared layer emits diverges from
-- Dolt's code-point order.

-- ============================================================ issues

CREATE TABLE IF NOT EXISTS issues (
    id                  text NOT NULL,
    content_hash        text,
    title               text NOT NULL,
    description         text NOT NULL,
    design              text NOT NULL,
    acceptance_criteria text NOT NULL,
    notes               text NOT NULL,
    status              text NOT NULL DEFAULT 'open',
    priority            integer NOT NULL DEFAULT 2,
    issue_type          text NOT NULL DEFAULT 'task',
    assignee            text,
    estimated_minutes   integer,
    created_at          timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    created_by          text DEFAULT '',
    owner               text DEFAULT '',
    updated_at          timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    closed_at           timestamp(0),
    closed_by_session   text DEFAULT '',
    external_ref        text,
    spec_id             text,
    compaction_level    integer DEFAULT 0,
    compacted_at        timestamp(0),
    compacted_at_commit text,
    original_size       integer,
    sender              text DEFAULT '',
    ephemeral           smallint DEFAULT 0,
    wisp_type           text DEFAULT '',
    pinned              smallint DEFAULT 0,
    is_template         smallint DEFAULT 0,
    mol_type            text DEFAULT '',
    work_type           text DEFAULT 'mutex',
    source_system       text DEFAULT '',
    metadata            jsonb DEFAULT '{}',
    source_repo         text DEFAULT '',
    close_reason        text DEFAULT '',
    event_kind          text DEFAULT '',
    actor               text DEFAULT '',
    target              text DEFAULT '',
    payload             text DEFAULT '',
    await_type          text DEFAULT '',
    await_id            text DEFAULT '',
    timeout_ns          bigint DEFAULT 0,
    waiters             text DEFAULT '',
    hook_bead           text DEFAULT '',
    role_bead           text DEFAULT '',
    agent_state         text DEFAULT '',
    last_activity       timestamp(0),
    role_type           text DEFAULT '',
    rig                 text DEFAULT '',
    due_at              timestamp(0),
    defer_until         timestamp(0),
    no_history          smallint DEFAULT 0,
    started_at          timestamp(0),
    is_blocked          smallint NOT NULL DEFAULT 0,
    lease_expires_at    timestamp(0),
    heartbeat_at        timestamp(0),
    row_lock            bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS idx_issues_assignee          ON issues (assignee);
CREATE INDEX IF NOT EXISTS idx_issues_created_at        ON issues (created_at);
CREATE INDEX IF NOT EXISTS idx_issues_defer_until       ON issues (defer_until);
CREATE INDEX IF NOT EXISTS idx_issues_external_ref      ON issues (external_ref);
CREATE INDEX IF NOT EXISTS idx_issues_is_blocked        ON issues (is_blocked, status);
CREATE INDEX IF NOT EXISTS idx_issues_issue_type        ON issues (issue_type);
CREATE INDEX IF NOT EXISTS idx_issues_priority          ON issues (priority);
CREATE INDEX IF NOT EXISTS idx_issues_spec_id           ON issues (spec_id);
CREATE INDEX IF NOT EXISTS idx_issues_status_updated_at ON issues (status, updated_at);
CREATE INDEX IF NOT EXISTS idx_issues_lease             ON issues (status, lease_expires_at);

-- ============================================================ wisps

CREATE TABLE IF NOT EXISTS wisps (
    id                  text NOT NULL,
    content_hash        text,
    title               text NOT NULL,
    description         text NOT NULL DEFAULT '',
    design              text NOT NULL DEFAULT '',
    acceptance_criteria text NOT NULL DEFAULT '',
    notes               text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'open',
    priority            integer NOT NULL DEFAULT 2,
    issue_type          text NOT NULL DEFAULT 'task',
    assignee            text,
    estimated_minutes   integer,
    created_at          timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    created_by          text DEFAULT '',
    owner               text DEFAULT '',
    updated_at          timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    closed_at           timestamp(0),
    closed_by_session   text DEFAULT '',
    external_ref        text,
    spec_id             text,
    compaction_level    integer DEFAULT 0,
    compacted_at        timestamp(0),
    compacted_at_commit text,
    original_size       integer,
    sender              text DEFAULT '',
    ephemeral           smallint DEFAULT 0,
    wisp_type           text DEFAULT '',
    pinned              smallint DEFAULT 0,
    is_template         smallint DEFAULT 0,
    mol_type            text DEFAULT '',
    work_type           text DEFAULT 'mutex',
    source_system       text DEFAULT '',
    metadata            jsonb DEFAULT '{}',
    source_repo         text DEFAULT '',
    close_reason        text DEFAULT '',
    event_kind          text DEFAULT '',
    actor               text DEFAULT '',
    target              text DEFAULT '',
    payload             text DEFAULT '',
    await_type          text DEFAULT '',
    await_id            text DEFAULT '',
    timeout_ns          bigint DEFAULT 0,
    waiters             text DEFAULT '',
    hook_bead           text DEFAULT '',
    role_bead           text DEFAULT '',
    agent_state         text DEFAULT '',
    last_activity       timestamp(0),
    role_type           text DEFAULT '',
    rig                 text DEFAULT '',
    due_at              timestamp(0),
    defer_until         timestamp(0),
    no_history          smallint DEFAULT 0,
    started_at          timestamp(0),
    is_blocked          smallint NOT NULL DEFAULT 0,
    lease_expires_at    timestamp(0),
    heartbeat_at        timestamp(0),
    row_lock            bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS idx_wisps_assignee     ON wisps (assignee);
CREATE INDEX IF NOT EXISTS idx_wisps_created_at   ON wisps (created_at);
CREATE INDEX IF NOT EXISTS idx_wisps_external_ref ON wisps (external_ref);
CREATE INDEX IF NOT EXISTS idx_wisps_is_blocked   ON wisps (is_blocked, status);
CREATE INDEX IF NOT EXISTS idx_wisps_issue_type   ON wisps (issue_type);
CREATE INDEX IF NOT EXISTS idx_wisps_priority     ON wisps (priority);
CREATE INDEX IF NOT EXISTS idx_wisps_spec_id      ON wisps (spec_id);
CREATE INDEX IF NOT EXISTS idx_wisps_status       ON wisps (status);

-- ============================================================ helper functions
-- No updated_at trigger: MySQL's ON UPDATE CURRENT_TIMESTAMP is intentionally
-- NOT reproduced. The shared layer assigns updated_at explicitly on every issues
-- / wisps UPDATE, including the is_blocked mark/unmark idiom that self-assigns
-- (SET updated_at = updated_at) precisely to SUPPRESS the auto-bump. A BEFORE
-- UPDATE trigger cannot see the statement's SET list, so it would stomp those
-- deliberately-suppressed writes to now() and corrupt synced timestamps on the
-- hottest dependency-state path; omitting it keeps PG identical to Dolt.

-- bd_mysql_jsonkey normalizes a MySQL JSON path to the bare top-level key the
-- translator binds for dynamic metadata filters: '$.sprint' -> sprint,
-- '$."gc.routed_to"' -> gc.routed_to. The translator rewrites
-- JSON_EXTRACT(metadata, ?) -> metadata -> bd_mysql_jsonkey(?) and
-- JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) -> metadata ->> bd_mysql_jsonkey(?).
-- storage.JSONMetadataPath always quotes dotted keys into one top-level segment,
-- so a single-key ->/->> lookup is exactly conformant with Dolt.
CREATE OR REPLACE FUNCTION bd_mysql_jsonkey(p text) RETURNS text LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN p LIKE '$."%"' THEN substring(p from 4 for length(p)-4) ELSE substring(p from 3) END $$;

-- ============================================================ labels

CREATE TABLE IF NOT EXISTS labels (
    issue_id text NOT NULL,
    label    text NOT NULL,
    PRIMARY KEY (issue_id, label),
    CONSTRAINT fk_labels_issue FOREIGN KEY (issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_labels_label ON labels (label);

-- ============================================================ wisp_labels

CREATE TABLE IF NOT EXISTS wisp_labels (
    issue_id text NOT NULL,
    label    text NOT NULL,
    PRIMARY KEY (issue_id, label),
    CONSTRAINT fk_wisp_labels_issue FOREIGN KEY (issue_id) REFERENCES wisps (id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_wisp_labels_label ON wisp_labels (label);

-- ============================================================ dependencies

CREATE TABLE IF NOT EXISTS dependencies (
    id                  text NOT NULL,
    issue_id            text NOT NULL,
    type                text NOT NULL DEFAULT 'blocks',
    created_at          timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    created_by          text NOT NULL,
    metadata            jsonb DEFAULT '{}',
    thread_id           text DEFAULT '',
    depends_on_issue_id text,
    depends_on_wisp_id  text,
    depends_on_external text,
    PRIMARY KEY (id),
    CONSTRAINT fk_dep_issue        FOREIGN KEY (issue_id)            REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT ck_dep_one_target CHECK (
        ((depends_on_issue_id IS NOT NULL)::integer
         + (depends_on_wisp_id IS NOT NULL)::integer
         + (depends_on_external IS NOT NULL)::integer) = 1
    )
);

CREATE INDEX IF NOT EXISTS idx_dep_external_target  ON dependencies (depends_on_external);
CREATE INDEX IF NOT EXISTS idx_dep_issue_target     ON dependencies (depends_on_issue_id);
CREATE INDEX IF NOT EXISTS idx_dep_type_external    ON dependencies (type, depends_on_external);
CREATE INDEX IF NOT EXISTS idx_dep_type_issue       ON dependencies (type, depends_on_issue_id);
CREATE INDEX IF NOT EXISTS idx_dep_type_wisp        ON dependencies (type, depends_on_wisp_id);
CREATE INDEX IF NOT EXISTS idx_dep_wisp_target      ON dependencies (depends_on_wisp_id);
CREATE INDEX IF NOT EXISTS idx_dependencies_issue   ON dependencies (issue_id);
CREATE INDEX IF NOT EXISTS idx_dependencies_thread  ON dependencies (thread_id);
-- MySQL unique keys treat NULLs as distinct; PG's default (NULLS DISTINCT) matches.
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_external_target ON dependencies (issue_id, depends_on_external);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_issue_target    ON dependencies (issue_id, depends_on_issue_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_dep_wisp_target     ON dependencies (issue_id, depends_on_wisp_id);

-- ============================================================ wisp_dependencies

CREATE TABLE IF NOT EXISTS wisp_dependencies (
    id                  text NOT NULL,
    issue_id            text NOT NULL,
    depends_on_issue_id text,
    depends_on_wisp_id  text,
    depends_on_external text,
    type                text NOT NULL DEFAULT 'blocks',
    created_at          timestamp(0) DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    created_by          text DEFAULT '',
    metadata            jsonb DEFAULT '{}',
    thread_id           text DEFAULT '',
    PRIMARY KEY (id),
    CONSTRAINT fk_wisp_dep_issue        FOREIGN KEY (issue_id)            REFERENCES wisps (id)  ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_wisp_dep_wisp_target  FOREIGN KEY (depends_on_wisp_id)  REFERENCES wisps (id)  ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT ck_wisp_dep_one_target CHECK (
        ((depends_on_issue_id IS NOT NULL)::integer
         + (depends_on_wisp_id IS NOT NULL)::integer
         + (depends_on_external IS NOT NULL)::integer) = 1
    )
);

CREATE INDEX IF NOT EXISTS fk_wisp_dep_issue_target    ON wisp_dependencies (depends_on_issue_id);
CREATE INDEX IF NOT EXISTS fk_wisp_dep_wisp_target     ON wisp_dependencies (depends_on_wisp_id);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type           ON wisp_dependencies (type);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_external  ON wisp_dependencies (type, depends_on_external);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_issue     ON wisp_dependencies (type, depends_on_issue_id);
CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_wisp      ON wisp_dependencies (type, depends_on_wisp_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_wisp_dep_external_target ON wisp_dependencies (issue_id, depends_on_external);
CREATE UNIQUE INDEX IF NOT EXISTS uk_wisp_dep_issue_target    ON wisp_dependencies (issue_id, depends_on_issue_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_wisp_dep_wisp_target     ON wisp_dependencies (issue_id, depends_on_wisp_id);

-- ============================================================ comments

CREATE TABLE IF NOT EXISTS comments (
    id         text NOT NULL,
    issue_id   text NOT NULL,
    author     text NOT NULL,
    text       text NOT NULL,
    created_at timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    PRIMARY KEY (id),
    CONSTRAINT fk_comments_issue FOREIGN KEY (issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_comments_created_at ON comments (created_at);
CREATE INDEX IF NOT EXISTS idx_comments_issue      ON comments (issue_id);

-- ============================================================ wisp_comments

CREATE TABLE IF NOT EXISTS wisp_comments (
    id         text NOT NULL,
    issue_id   text NOT NULL,
    author     text DEFAULT '',
    text       text NOT NULL,
    created_at timestamp(0) DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    PRIMARY KEY (id),
    CONSTRAINT fk_wisp_comments_issue FOREIGN KEY (issue_id) REFERENCES wisps (id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_wisp_comments_issue ON wisp_comments (issue_id);

-- ============================================================ events

CREATE TABLE IF NOT EXISTS events (
    id         text NOT NULL,
    issue_id   text NOT NULL,
    event_type text NOT NULL,
    actor      text NOT NULL,
    old_value  text,
    new_value  text,
    comment    text,
    created_at timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    PRIMARY KEY (id),
    CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_events_created_at ON events (created_at);
CREATE INDEX IF NOT EXISTS idx_events_issue      ON events (issue_id);

-- ============================================================ wisp_events

CREATE TABLE IF NOT EXISTS wisp_events (
    id         text NOT NULL,
    issue_id   text NOT NULL,
    event_type text NOT NULL,
    actor      text DEFAULT '',
    old_value  text DEFAULT '',
    new_value  text DEFAULT '',
    comment    text DEFAULT '',
    created_at timestamp(0) DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    PRIMARY KEY (id),
    CONSTRAINT fk_wisp_events_issue FOREIGN KEY (issue_id) REFERENCES wisps (id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_wisp_events_created_at ON wisp_events (created_at);
CREATE INDEX IF NOT EXISTS idx_wisp_events_issue      ON wisp_events (issue_id);

-- ============================================================ config

CREATE TABLE IF NOT EXISTS config (
    key   text NOT NULL,
    value text NOT NULL,
    PRIMARY KEY (key)
);

-- ============================================================ metadata

CREATE TABLE IF NOT EXISTS metadata (
    key   text NOT NULL,
    value text NOT NULL,
    PRIMARY KEY (key)
);

-- ============================================================ local_metadata

CREATE TABLE IF NOT EXISTS local_metadata (
    key   text NOT NULL,
    value text NOT NULL DEFAULT '',
    PRIMARY KEY (key)
);

-- ============================================================ issue_counter

CREATE TABLE IF NOT EXISTS issue_counter (
    prefix  text NOT NULL,
    last_id integer NOT NULL DEFAULT 0,
    PRIMARY KEY (prefix)
);

-- ============================================================ child_counters

CREATE TABLE IF NOT EXISTS child_counters (
    parent_id  text NOT NULL,
    last_child integer NOT NULL DEFAULT 0,
    PRIMARY KEY (parent_id),
    CONSTRAINT fk_counter_parent FOREIGN KEY (parent_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE
);

-- ============================================================ wisp_child_counters

CREATE TABLE IF NOT EXISTS wisp_child_counters (
    parent_id  text NOT NULL,
    last_child integer NOT NULL DEFAULT 0,
    PRIMARY KEY (parent_id),
    CONSTRAINT fk_wisp_child_counters_parent FOREIGN KEY (parent_id) REFERENCES wisps (id) ON DELETE CASCADE ON UPDATE CASCADE
);

-- ============================================================ custom_statuses

CREATE TABLE IF NOT EXISTS custom_statuses (
    name     text NOT NULL,
    category text NOT NULL DEFAULT 'unspecified',
    PRIMARY KEY (name)
);

-- ============================================================ custom_types

CREATE TABLE IF NOT EXISTS custom_types (
    name text NOT NULL,
    PRIMARY KEY (name)
);

-- ============================================================ repo_mtimes

CREATE TABLE IF NOT EXISTS repo_mtimes (
    repo_path    text NOT NULL,
    jsonl_path   text NOT NULL,
    mtime_ns     bigint NOT NULL,
    last_checked timestamp(0) NOT NULL DEFAULT (date_trunc('second', now() AT TIME ZONE 'utc')),
    PRIMARY KEY (repo_path)
);

CREATE INDEX IF NOT EXISTS idx_repo_mtimes_checked ON repo_mtimes (last_checked);
`

// schemaVersion is stamped into the metadata table by InitSchema. A workspace
// carrying a different stamp is rejected: this proof-wedge ships no migrator.
const schemaVersion = "1"

// schemaVersionKey lives in the metadata table (NOT config): config rows are
// surfaced by issueops.GetAllConfig / `bd config list`, so a version key there
// would leak a permanent diff against the Dolt oracle in differential runs. The
// metadata table is where the shared layer already keeps backend-internal keys.
const schemaVersionKey = "pg_schema_version"

// schemaNameRe restricts schema names to a plain SQL identifier so they can be
// safely interpolated into CREATE SCHEMA (which cannot take a placeholder).
var schemaNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// InitSchema creates the per-workspace schema if absent, then applies the
// embedded DDL one statement at a time and stamps the schema version.
//
// db must be a RAW *sql.DB (pgdialect.OpenRaw), not the translating one: the DDL
// is already native Postgres and routing it through Translate mangles the
// $$-quoted function bodies and rewrites now(). Version-stamp queries below use
// native $n placeholders for the same reason.
//
// All work runs on a single connection whose search_path is pinned to the target
// schema, so DDL never silently lands in public when db is handed unpinned. The
// schema name is validated (not parameterizable in DDL) and every DDL statement
// is executed on its own, since pgx's extended protocol rejects multiple
// statements in a single Exec.
func InitSchema(ctx context.Context, db *sql.DB, schema string) error {
	if !schemaNameRe.MatchString(schema) {
		return fmt.Errorf("postgres: invalid schema name %q", schema)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS "`+schema+`"`); err != nil {
		return fmt.Errorf("postgres: create schema %q: %w", schema, err)
	}
	if _, err := conn.ExecContext(ctx, `SET search_path TO "`+schema+`"`); err != nil {
		return fmt.Errorf("postgres: set search_path %q: %w", schema, err)
	}
	for _, stmt := range splitStatements(ddl) {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("postgres: exec DDL statement: %w\n-- statement:\n%s", err, stmt)
		}
	}
	fresh, err := stampSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	// Seed the default config rows ONLY on first provision — exactly like Dolt's
	// migration 0016, which runs once. InitSchema runs on EVERY open (NewFromConfig →
	// Provision → InitSchema) for idempotency, so seeding unconditionally would
	// resurrect any config key a user has `config unset`, diverging from Dolt.
	if fresh {
		if err := seedDefaultConfig(ctx, conn); err != nil {
			return err
		}
	}
	return nil
}

// defaultConfigSeeds mirrors internal/storage/schema/migrations/0016_default_config.up.sql
// exactly — the config rows a fresh Dolt workspace materializes on init. Seeding the
// identical set (and no more) keeps `bd config list` byte-identical to the Dolt oracle;
// on Dolt these land via a migration, which the wedge does not run, so InitSchema seeds
// them directly. issue_prefix is intentionally absent: it is workspace-specific and the
// caller (bd init) seeds it via store.SetConfig, matching the Dolt path.
var defaultConfigSeeds = [][2]string{
	{"compaction_enabled", "false"},
	{"compact_tier1_days", "30"},
	{"compact_tier1_dep_levels", "2"},
	{"compact_tier2_days", "90"},
	{"compact_tier2_dep_levels", "5"},
	{"compact_tier2_commits", "100"},
	{"compact_batch_size", "50"},
	{"compact_parallel_workers", "5"},
	{"auto_compact_enabled", "false"},
}

// seedDefaultConfig inserts the migration-0016 default config rows using
// ON CONFLICT DO NOTHING (the Postgres equivalent of the migration's INSERT IGNORE).
// InitSchema calls it only on first provision; the ON CONFLICT guard additionally
// makes it safe against a concurrent first init. Runs on the search_path-pinned
// connection so it targets the workspace config table.
func seedDefaultConfig(ctx context.Context, conn *sql.Conn) error {
	for _, kv := range defaultConfigSeeds {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO config (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`,
			kv[0], kv[1]); err != nil {
			return fmt.Errorf("postgres: seed default config %q: %w", kv[0], err)
		}
	}
	return nil
}

// stampSchemaVersion records schemaVersion in the metadata table on first init
// and refuses to open a workspace written by a binary with a different schema
// version (the proof-wedge has no migrator). It returns fresh=true only when it
// stamped a brand-new schema (no version row yet) — the signal InitSchema uses to
// seed one-time data exactly once. Runs on the search_path-pinned connection so it
// resolves the workspace metadata table.
func stampSchemaVersion(ctx context.Context, conn *sql.Conn) (fresh bool, err error) {
	var stored string
	err = conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = $1`, schemaVersionKey).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Provision runs on every open, so concurrent first opens of the same
		// workspace (e.g. the hosted server from two goroutines) can both land
		// here. ON CONFLICT DO NOTHING lets both succeed; only the opener that
		// actually inserted the row (RowsAffected==1) is fresh and seeds defaults.
		res, ierr := conn.ExecContext(ctx, `INSERT INTO metadata (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, schemaVersionKey, schemaVersion)
		if ierr != nil {
			return false, fmt.Errorf("postgres: stamp schema version: %w", ierr)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return true, nil
		}
		// Lost the race: re-read what the winner stored and version-check it.
		if rerr := conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = $1`, schemaVersionKey).Scan(&stored); rerr != nil {
			return false, fmt.Errorf("postgres: read schema version after conflict: %w", rerr)
		}
		if stored != schemaVersion {
			return false, fmt.Errorf("postgres: workspace schema version %s, this binary requires %s — no migrator in the proof-wedge, recreate the workspace or use a matching binary", stored, schemaVersion)
		}
		return false, nil
	case err != nil:
		return false, fmt.Errorf("postgres: read schema version: %w", err)
	case stored != schemaVersion:
		return false, fmt.Errorf("postgres: workspace schema version %s, this binary requires %s — no migrator in the proof-wedge, recreate the workspace or use a matching binary", stored, schemaVersion)
	default:
		return false, nil
	}
}

// splitStatements splits the DDL into individual statements on top-level ';'
// boundaries. It ignores ';' that appears inside a $$-delimited body (the
// trigger function), inside a single-quoted string literal, or inside a '--'
// line comment; line comments are dropped so a ';' in prose never terminates a
// statement. Blank fragments are omitted.
func splitStatements(ddl string) []string {
	var stmts []string
	var buf strings.Builder
	var inString, inDollar bool
	n := len(ddl)
	for i := 0; i < n; i++ {
		c := ddl[i]
		switch {
		case inString:
			buf.WriteByte(c)
			if c == '\'' {
				if i+1 < n && ddl[i+1] == '\'' {
					buf.WriteByte(ddl[i+1])
					i++
					continue
				}
				inString = false
			}
		case inDollar:
			if c == '$' && i+1 < n && ddl[i+1] == '$' {
				buf.WriteString("$$")
				inDollar = false
				i++
				continue
			}
			buf.WriteByte(c)
		default:
			switch {
			case c == '-' && i+1 < n && ddl[i+1] == '-':
				for i < n && ddl[i] != '\n' {
					i++
				}
				// Leave i at the newline (or end); the loop's i++ advances past it.
			case c == '$' && i+1 < n && ddl[i+1] == '$':
				buf.WriteString("$$")
				inDollar = true
				i++
			case c == '\'':
				inString = true
				buf.WriteByte(c)
			case c == ';':
				if s := strings.TrimSpace(buf.String()); s != "" {
					stmts = append(stmts, s)
				}
				buf.Reset()
			default:
				buf.WriteByte(c)
			}
		}
	}
	if s := strings.TrimSpace(buf.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
