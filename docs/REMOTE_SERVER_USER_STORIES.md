# Remote Dolt Server — User Stories

Stories describing expected behavior when beads connects to a remote dolt server
(e.g., `host: mini2`) instead of localhost. These drive the fix in PR #3595.

## Push/Pull

```gherkin
Given beads is connected to a remote dolt server (mini2)
When I run `bd dolt push`
Then the push command is sent via SQL to the remote server
And the remote server executes the push against its own remotes
And I see "Push complete."

Given beads is connected to a remote dolt server
When I run `bd dolt pull`
Then the pull command is sent via SQL to the remote server
And the remote server pulls from its own remotes
And my next query sees the updated data

Given beads is connected to a remote dolt server with no remotes configured
When I run `bd dolt push`
Then I see a clear error explaining no remotes are configured on the server
And NOT a raw SQL error or stack trace

Given beads is connected to a remote dolt server
When I run `bd dolt push --force`
Then the force flag is passed through the SQL path
And the remote server executes a force push

Given beads is connected to a remote dolt server
When I run `bd dolt push --remote backup`
Then the named remote is passed through the SQL path
And the remote server pushes to the "backup" remote
```

## Backup

```gherkin
Given beads is connected to a remote dolt server
When I run `bd backup export` without specifying a location
Then beads errors with a clear message explaining that server mode
  requires an explicit backup location
And suggests: `bd backup export --output /path/to/backup.jsonl`

Given beads is connected to a remote dolt server
When I run `bd backup export --output ~/backups/beads.jsonl`
Then beads exports issues as JSONL to the specified local path
And does NOT attempt to use Dolt's filesystem backup mechanism

Given beads is connected to a remote dolt server
When I run `bd backup init /some/local/path`
Then beads does NOT send that local path to the remote server
And instead tells me filesystem backup is not supported in remote mode
And suggests using a cloud URL (DoltHub, S3, GCS) instead

Given beads is connected to a remote dolt server
When I run `bd backup init https://doltremoteapi.dolthub.com/user/repo`
Then the DoltHub URL is passed through to the remote server
And the remote server registers it as a backup destination

Given beads is connected to a local dolt server (localhost)
When I run `bd backup init /some/local/path`
Then the backup is created at that path as normal

Given beads is connected to a remote dolt server
When I have a JSONL backup file
And I run `bd init --from-jsonl backup.jsonl`
Then the issues are imported into the remote server via SQL
```

## Info/Diagnostics

```gherkin
Given beads is connected to a remote dolt server
When I run `bd dolt show`
Then remotes are queried via SQL (SELECT * FROM dolt_remotes)
And I see the server's actual configured remotes
And it does NOT say "no remotes found" just because there's no local dolt dir

Given beads is connected to a remote dolt server
When I run `bd doctor`
Then checks that require local filesystem access show SKIP (remote mode)
And I do NOT see misleading errors about missing directories
And I can see which checks were skipped and why

Given beads is connected to a remote dolt server
And the SQL connection is down
When I run any bd command
Then I see "Cannot connect to dolt server at <host>:<port>" within a reasonable timeout
And NOT a hang or raw stack trace
```

## Config Conflicts

```gherkin
Given config.yaml specifies host: mini2
And BEADS_DOLT_CLI_DIR is also set
When beads initializes
Then [DECISION NEEDED: error at startup? CLI dir wins? remote wins with warning?]

Given beads is connected to a dolt server on localhost via SQL (not embedded mode)
When I run `bd backup init /some/local/path`
Then the filesystem backup works because the server CAN access local paths
```

## General Principle

```gherkin
Given beads is connected to a remote dolt server
When any command needs to interact with the dolt database or its version control
Then it uses SQL (network) not filesystem (local)
And it never sends local filesystem paths to the remote server
And it never shells out to a local dolt CLI expecting a local database

# Exception: .beads/ config, hooks, JSONL exports are always local operations
```

## Out of Scope (follow-up work)

- SQL-mode vs CLI-mode refactor (deeper than remote host detection)
- Server version mismatch detection
- Read-only SQL user handling
- Federation peer sync in remote mode
- Connection timeout tuning
