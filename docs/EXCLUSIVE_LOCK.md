# Exclusive Lock Protocol

The exclusive lock protocol allows external tools to claim exclusive management of a beads database, preventing the bd daemon from interfering with their operations.

## Use Cases

- **Deterministic execution systems** (e.g., VibeCoder) that need full control over database state
- **CI/CD pipelines** that perform atomic issue updates without daemon interference
- **Custom automation tools** that manage their own git sync workflow

## How It Works

### Lock File Format

The lock file is located at `.beads/.exclusive-lock` and contains JSON:

```json
{
  "holder": "vc-executor",
  "pid": 12345,
  "hostname": "dev-machine",
  "started_at": "2025-10-25T12:00:00Z",
  "version": "1.0.0"
}
```

**Fields:**
- `holder` (string, required): Name of the tool holding the lock (e.g., "vc-executor", "ci-runner")
- `pid` (int, required): Process ID of the lock holder
- `hostname` (string, required): Hostname where the process is running
- `started_at` (RFC3339 timestamp, required): When the lock was acquired
- `version` (string, optional): Version of the lock holder

### Daemon Behavior

The bd daemon checks for exclusive locks at the start of each sync cycle:

1. **No lock file**: Daemon proceeds normally with sync operations
2. **Valid lock (process alive)**: Daemon skips all operations for this database
3. **Stale lock (process dead)**: Daemon removes the lock and proceeds
4. **Malformed lock**: Daemon fails safe and skips the database

### Stale Lock Detection

A lock is considered stale if:
- The hostname matches the current machine (case-insensitive) AND
- The PID does not exist on the local system (returns ESRCH)

**Important:** The daemon only removes locks when it can definitively determine the process is dead (ESRCH error). If the daemon lacks permission to signal a PID (EPERM), it treats the lock as valid and skips the database. This fail-safe approach prevents accidentally removing locks owned by other users.

**Remote locks** (different hostname) are always assumed to be valid since the daemon cannot verify remote processes.

When a stale lock is successfully removed, the daemon logs: `Removed stale lock (holder-name), proceeding with sync`

## Usage Examples

### Creating a Lock (Go)

```go
import (
    "encoding/json"
    "os"
    "path/filepath"
    "github.com/steveyegge/beads/internal/types"
)

func acquireLock(beadsDir, holder, version string) error {
    lock, err := types.NewExclusiveLock(holder, version)
    if err != nil {
        return err
    }

    data, err := json.MarshalIndent(lock, "", "  ")
    if err != nil {
        return err
    }

    lockPath := filepath.Join(beadsDir, ".exclusive-lock")
    return os.WriteFile(lockPath, data, 0644)
}
```

### Releasing a Lock (Go)

```go
func releaseLock(beadsDir string) error {
    lockPath := filepath.Join(beadsDir, ".exclusive-lock")
    return os.Remove(lockPath)
}
```

### Creating a Lock (Shell)

```bash
#!/bin/bash
BEADS_DIR=".beads"
LOCK_FILE="$BEADS_DIR/.exclusive-lock"

# Create lock
cat > "$LOCK_FILE" <<EOF
{
  "holder": "my-tool",
  "pid": $$,
  "hostname": "$(hostname)",
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "version": "1.0.0"
}
EOF

# Do work...
bd create "My issue" -p 1
bd update bd-42 --status in_progress

# Release lock
rm "$LOCK_FILE"
```

### Recommended Pattern

Always use cleanup handlers to ensure locks are released:

```go
func main() {
    beadsDir := ".beads"
    
    // Acquire lock
    if err := acquireLock(beadsDir, "my-tool", "1.0.0"); err != nil {
        log.Fatal(err)
    }
    
    // Ensure lock is released on exit
    defer func() {
        if err := releaseLock(beadsDir); err != nil {
            log.Printf("Warning: failed to release lock: %v", err)
        }
    }()
    
    // Do work with beads database...
}
```

## Edge Cases and Limitations

### Multiple Writers Without Daemon

The exclusive lock protocol **only prevents daemon interference**. It does NOT provide:
- ❌ Mutual exclusion between multiple external tools
- ❌ Transaction isolation or ACID guarantees
- ❌ Protection against direct file system manipulation

If you need coordination between multiple tools, implement your own locking mechanism.

### Git Worktrees

The daemon already has issues with git worktrees (see AGENTS.md). The exclusive lock protocol doesn't solve this—use `--no-daemon` mode in worktrees instead.

### Remote Hosts

Locks from remote hosts are always assumed valid because the daemon cannot verify remote PIDs. This means:
- Stale locks from remote hosts will **not** be automatically cleaned up
- You must manually remove stale remote locks

### Lock File Corruption

If the lock file becomes corrupted (invalid JSON), the daemon **fails safe** and skips the database. You must manually fix or remove the lock file.

## Daemon Logging

The daemon logs lock-related events:

```
Skipping database (locked by vc-executor)
Removed stale lock (vc-executor), proceeding with sync
Skipping database (lock check failed: malformed lock file: unexpected EOF)
```

Check daemon logs (default: `.beads/daemon.log`) to troubleshoot lock issues.

**Note:** The daemon checks for locks at the start of each sync cycle. If a lock is created during a sync cycle, that cycle will complete, but subsequent cycles will skip the database.

## Testing Your Integration

1. **Start the daemon**: `bd daemon start --interval 1m`
2. **Create a lock**: Use your tool to create `.beads/.exclusive-lock`
3. **Verify daemon skips**: Check daemon logs for "Skipping database" message
4. **Release lock**: Remove `.beads/.exclusive-lock`
5. **Verify daemon resumes**: Check daemon logs for normal sync cycle

## Security Considerations

- Lock files are **not secure**. Any process can create, modify, or delete them.
- PID reuse could theoretically cause issues (very rare, especially with hostname check)
- This is a **cooperative** protocol, not a security mechanism

## API Reference

### Go Types

```go
// ExclusiveLock represents the lock file format
type ExclusiveLock struct {
    Holder    string    `json:"holder"`
    PID       int       `json:"pid"`
    Hostname  string    `json:"hostname"`
    StartedAt time.Time `json:"started_at"`
    Version   string    `json:"version"`
}

// NewExclusiveLock creates a lock for the current process
func NewExclusiveLock(holder, version string) (*ExclusiveLock, error)

// Validate checks if the lock has valid field values
func (e *ExclusiveLock) Validate() error

// ShouldSkipDatabase checks if database should be skipped due to lock
func ShouldSkipDatabase(beadsDir string) (skip bool, holder string, err error)

// IsProcessAlive checks if a process is running
func IsProcessAlive(pid int, hostname string) bool
```

## Questions?

For integration help, see:
- **AGENTS.md** - General workflow guidance
- **README.md** - Daemon configuration
- **examples/** - Sample integrations

File issues at: https://github.com/steveyegge/beads/issues
