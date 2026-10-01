-- label_definitions: the opt-in curated label vocabulary a workspace can
-- declare via `bd label define`. Its presence changes nothing about what
-- label a caller may write by itself -- whether a write is checked against it
-- (and whether an undefined label warns or is refused) is controlled by the
-- labels.vocabulary config knob (open|warn|enforce, default open; see
-- docs/core-concepts/labels.md). `bd label add`/`create --labels`/`update
-- --add-label`/`tag`/`quick` consult it; import/replay never does.
--
-- Same shape as custom_statuses/custom_types (0024): a bare name table with
-- no relation to `issues`, so defining or undefining a label never touches a
-- row already carrying that label on an issue.
--
-- Case handling: `bd label define`/`undefine` reject a case-insensitive
-- collision against an EXISTING row (defining "Backend" when "backend" is
-- already defined is an error naming the existing spelling), so this table
-- never holds two case-variant spellings of the same word -- but that
-- discipline is a check-then-insert in application code, and a check cannot
-- see a concurrent writer's row that has not committed yet. label_folded is
-- the DB-level backstop: a column derived from `label` the SAME way the
-- application folds it (Go's strings.ToLower, the single folding authority
-- -- see issueops.DefineLabelInTx), carrying its own UNIQUE constraint so two
-- transactions racing to define case-variants of the same word cannot both
-- land, regardless of how their application-level pre-checks interleaved.
-- `label` itself keeps its default (case-sensitive/binary) collation as the
-- primary key; label_folded is what makes the case-insensitive constraint an
-- invariant of the SCHEMA rather than a promise application code keeps.
--
-- It does NOT fold or rewrite labels already stored on issues in a different
-- case -- that is `bd label rename`'s job, a separate capability -- so a
-- label written before its definition existed, in a different case, is only
-- ever reported by `bd doctor` as a case-variant cluster, never silently
-- merged here.
CREATE TABLE IF NOT EXISTS label_definitions (
    label VARCHAR(255) NOT NULL PRIMARY KEY,
    label_folded VARCHAR(255) NOT NULL,
    description TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255),
    UNIQUE KEY uk_label_definitions_folded (label_folded)
);
