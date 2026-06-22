-- Repair rig identity beads that earlier defaults routed into the ephemeral
-- wisps tier. Rig beads are durable issue state: keep type=rig hidden from
-- ready work in application code, but promote existing rows back to issues.

SET FOREIGN_KEY_CHECKS = 0;

INSERT IGNORE INTO issues (
    id, content_hash, title, description, design, acceptance_criteria, notes,
    status, priority, issue_type, assignee, estimated_minutes, created_at,
    created_by, owner, updated_at, closed_at, closed_by_session, external_ref,
    spec_id, compaction_level, compacted_at, compacted_at_commit, original_size,
    sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type,
    source_system, metadata, source_repo, close_reason, event_kind, actor,
    target, payload, await_type, await_id, timeout_ns, waiters, hook_bead,
    role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until,
    no_history, started_at
)
SELECT
    id, content_hash, title, description, design, acceptance_criteria, notes,
    status, priority, issue_type, assignee, estimated_minutes, created_at,
    created_by, owner, updated_at, closed_at, closed_by_session, external_ref,
    spec_id, compaction_level, compacted_at, compacted_at_commit, original_size,
    sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type,
    source_system, metadata, source_repo, close_reason, event_kind, actor,
    target, payload, await_type, await_id, timeout_ns, waiters, hook_bead,
    role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until,
    no_history, started_at
FROM wisps
WHERE issue_type = 'rig';

UPDATE issues
SET ephemeral = 0
WHERE issue_type = 'rig'
  AND id IN (SELECT id FROM wisps WHERE issue_type = 'rig');

INSERT IGNORE INTO labels (issue_id, label)
SELECT wl.issue_id, wl.label
FROM wisp_labels wl
JOIN wisps w ON w.id = wl.issue_id
WHERE w.issue_type = 'rig';

INSERT IGNORE INTO dependencies (
    id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
    type, created_at, created_by, metadata, thread_id
)
SELECT
    wd.id, wd.issue_id, wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external,
    wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id
FROM wisp_dependencies wd
JOIN wisps w ON w.id = wd.issue_id
WHERE w.issue_type = 'rig';

INSERT IGNORE INTO events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
SELECT we.id, we.issue_id, we.event_type, we.actor, we.old_value, we.new_value, we.comment, we.created_at
FROM wisp_events we
JOIN wisps w ON w.id = we.issue_id
WHERE w.issue_type = 'rig';

INSERT IGNORE INTO comments (id, issue_id, author, text, created_at)
SELECT wc.id, wc.issue_id, wc.author, wc.text, wc.created_at
FROM wisp_comments wc
JOIN wisps w ON w.id = wc.issue_id
WHERE w.issue_type = 'rig';

CREATE TABLE IF NOT EXISTS wisp_child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0
);

INSERT IGNORE INTO child_counters (parent_id, last_child)
SELECT wcc.parent_id, wcc.last_child
FROM wisp_child_counters wcc
JOIN wisps w ON w.id = wcc.parent_id
WHERE w.issue_type = 'rig';

UPDATE child_counters cc
JOIN wisp_child_counters wcc ON wcc.parent_id = cc.parent_id
JOIN wisps w ON w.id = wcc.parent_id
SET cc.last_child = GREATEST(cc.last_child, wcc.last_child)
WHERE w.issue_type = 'rig';

DELETE wd FROM wisp_dependencies wd
JOIN wisps w ON w.id = wd.issue_id
WHERE w.issue_type = 'rig';

REPLACE INTO dependencies (
    id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
    type, created_at, created_by, metadata, thread_id
)
SELECT
    d.id, d.issue_id, d.depends_on_wisp_id, NULL, d.depends_on_external,
    d.type, d.created_at, d.created_by, d.metadata, d.thread_id
FROM dependencies d
JOIN wisps w ON w.id = d.depends_on_wisp_id
WHERE w.issue_type = 'rig';

REPLACE INTO wisp_dependencies (
    id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
    type, created_at, created_by, metadata, thread_id
)
SELECT
    wd.id, wd.issue_id, wd.depends_on_wisp_id, NULL, wd.depends_on_external,
    wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id
FROM wisp_dependencies wd
JOIN wisps w ON w.id = wd.depends_on_wisp_id
WHERE w.issue_type = 'rig';

DELETE wl FROM wisp_labels wl
JOIN wisps w ON w.id = wl.issue_id
WHERE w.issue_type = 'rig';

DELETE we FROM wisp_events we
JOIN wisps w ON w.id = we.issue_id
WHERE w.issue_type = 'rig';

DELETE wc FROM wisp_comments wc
JOIN wisps w ON w.id = wc.issue_id
WHERE w.issue_type = 'rig';

DELETE wcc FROM wisp_child_counters wcc
JOIN wisps w ON w.id = wcc.parent_id
WHERE w.issue_type = 'rig';

DELETE FROM wisps WHERE issue_type = 'rig';

SET FOREIGN_KEY_CHECKS = 1;
