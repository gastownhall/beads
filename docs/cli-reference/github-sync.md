---
title: "bd github-sync"
description: "Manage secure GitHub/GitLab authentication for Dolt sync"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc github-sync`.

Manage secure authentication when syncing beads Dolt data with
GitHub or GitLab remotes.

bd prefers the official gh and glab CLIs because they store credentials in
the OS keyring. If neither CLI is available, bd can perform an OAuth device
flow and store the token in the OS keyring itself.

For self-managed hosts (GitHub Enterprise or self-managed GitLab) pass
--host; gh and glab handle the host natively. The OAuth device flow targets
github.com and GitLab-compatible endpoints, so for GitHub Enterprise use
'--provider gh'.

Run 'bd github-sync status' to see which authentication methods are available
and 'bd github-sync login --provider gh' (or glab/oauth) to authenticate.

```
bd github-sync [flags]
bd github-sync [command]
```

**Flags:**

```
      --dry-run           Show what would happen without making changes
      --host string       Git host (default: inferred from remote, or github.com for login)
      --provider string   Authentication provider: gh, glab, oauth, or auto (default "auto")
```

## bd github-sync login

Log in to the configured host using the chosen provider.

For gh/glab, this invokes the CLI's interactive login. For OAuth, this runs
an OAuth device flow and prints a URL and user code.

```
bd github-sync login [flags]
```

**Flags:**

```
      --dry-run           Show what would happen without making changes
      --host string       Git host (default: inferred from remote, or github.com for login)
      --provider string   Authentication provider: gh, glab, oauth, or auto (default "auto")
```

## bd github-sync logout

Log out from the configured host using the chosen provider.

```
bd github-sync logout [flags]
```

**Flags:**

```
      --dry-run           Show what would happen without making changes
      --host string       Git host (default: inferred from remote, or github.com for login)
      --provider string   Authentication provider: gh, glab, oauth, or auto (default "auto")
```

## bd github-sync status

Show authentication status for a host

```
bd github-sync status [flags]
```

**Flags:**

```
      --dry-run           Show what would happen without making changes
      --host string       Git host (default: inferred from remote, or github.com for login)
      --provider string   Authentication provider: gh, glab, oauth, or auto (default "auto")
```
