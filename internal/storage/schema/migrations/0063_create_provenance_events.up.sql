-- provenance_events: a log binding an issue to a structured external artifact
-- (a git SHA, a PR, a work-id, a transcript, a branch). Append-only at the event
-- level — there is no UPDATE or DELETE operation on individual events. An event's
-- lifecycle is bound to its issue: it is removed only if the issue itself is
-- deleted (ON DELETE CASCADE), identical to the `events` audit table (0005).
--
-- This is a separate table from `events` on purpose. `events` is the hot
-- field-mutation audit trail (old_value/new_value pairs); provenance_events is
-- a typed binding to an opaque external reference produced by any runtime
-- (git hook, orchestrator, CI). bd never interprets `actor` or `ref` — they are
-- opaque identifiers — so the table stays a primitive usable without any
-- specific orchestrator. Only `kind` and `ref_kind` are structurally validated.
--
-- occurred_at (event-time) is separate from created_at (ingest-time): a producer
-- may record a fact after it happened (a git hook firing on a past commit).
--
-- `ref` is VARCHAR(255) rather than TEXT so it is directly indexable in
-- dolt/MySQL; a 40-char SHA or a PR URL fits comfortably. Refs longer than 255
-- chars are not supported by this column and should not be recorded here.
CREATE TABLE IF NOT EXISTS provenance_events (
    id CHAR(36) NOT NULL PRIMARY KEY,
    issue_id VARCHAR(255) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    actor VARCHAR(255),
    ref VARCHAR(255),
    ref_kind VARCHAR(32),
    payload TEXT,
    source VARCHAR(64) NOT NULL,
    occurred_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_provenance_events_issue_occurred (issue_id, occurred_at),
    INDEX idx_provenance_events_ref (ref),
    INDEX idx_provenance_events_kind (kind),
    CONSTRAINT fk_provenance_events_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE
);
