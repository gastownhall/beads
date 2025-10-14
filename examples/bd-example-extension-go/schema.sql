CREATE TABLE IF NOT EXISTS example_executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT NOT NULL,
    status TEXT NOT NULL,
    agent_id TEXT,
    started_at DATETIME,
    completed_at DATETIME,
    error TEXT,
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS example_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id INTEGER NOT NULL,
    phase TEXT NOT NULL,
    checkpoint_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (execution_id) REFERENCES example_executions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_executions_issue ON example_executions(issue_id);
CREATE INDEX IF NOT EXISTS idx_executions_status ON example_executions(status);
CREATE INDEX IF NOT EXISTS idx_checkpoints_execution ON example_checkpoints(execution_id);
