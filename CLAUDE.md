# Emma — Beads Crew

Private memory for the emma worktree/rig.

## Release Engineering

Emma handles beads releases. Key lessons learned:

### GitHub API Rate Limit (CRITICAL)

The GitHub API rate limit is **5000 requests/hour**. This has been exhausted
multiple times during releases by polling CI status. When the rate limit is
hit, ALL crew members are blocked from GitHub API access for up to an hour.

**NEVER do any of these during a release:**
- `gh run watch` — polls every 3 seconds (1200 req/hr per invocation)
- Background monitors polling `gh run view` or `gh run list` in loops
- Any automated polling of CI status at intervals less than 5 minutes
- Running multiple concurrent API-calling processes

**INSTEAD:**
- After pushing a tag, `sleep` for 10-15 minutes, then check ONCE
- Use `gh run view <run-id>` for a single status check
- If CI is still running, sleep another 10 minutes and check again
- Budget: 3-5 total API calls for the entire CI wait period
- Check `gh api rate_limit` if unsure about remaining quota

### Release Workflow YAML

The release workflow (`.github/workflows/release.yml`) uses zig cross-compilation
wrappers. Do NOT use shell heredocs (`<<'EOF'`) inside `run: |` YAML blocks —
the heredoc content at column 0 breaks YAML literal block parsing. Use `echo`
commands instead. This was the root cause of the v0.55.0 release failure.

### Version Bumping

- Use `scripts/update-versions.sh X.Y.Z` for version bumps
- The script uses version.go as the source of truth for the current version
- If any file is out of sync (e.g., marketplace.json missed), fix it manually
  to match current version BEFORE running the bump script
- Always run `scripts/check-versions.sh` to verify consistency
- Never reuse a tag that failed CI — bump to the next patch version instead

### Release Workflow (goreleaser)

- GoReleaser builds with `--parallelism 1` to avoid zig race conditions
- Zig 0.14.0 required (0.13.0 has AccessDenied bug)
- macOS builds need `-lresolv.9` workaround (zig can't find `-lresolv`)
- Full build takes 10-20 minutes due to serialized cross-compilation
