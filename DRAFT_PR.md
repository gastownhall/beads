## Fix: `~/.config/bd/config.yaml` not loaded on macOS

### Summary

- The docs say to put config at `~/.config/bd/config.yaml`, but on macOS beads only checks `~/Library/Application Support/bd/config.yaml`. If you follow the docs on a Mac, your config is silently ignored.
- This adds `~/.config/bd/config.yaml` as a config source on all platforms. On Linux it's already checked via `os.UserConfigDir()`, so a dedup guard prevents loading it twice. On Windows it adds `C:\Users\<name>\.config\bd\` alongside `AppData\Roaming\bd\`.
- If the file doesn't exist, nothing changes. No new behavior for users without it.

### Test plan

- [x] Config at `~/.config/bd/` loads when the platform default points elsewhere (macOS case)
- [x] No double-load on Linux where the platform default is already `~/.config`
- [x] No error when `~/.config/bd/config.yaml` doesn't exist
- [x] All config tests pass
- [x] Clean build with `CGO_ENABLED=0 go build -tags nocgo ./...`
- [ ] Manual: create `~/.config/bd/config.yaml` on a Mac, confirm `bd` picks it up

### Config priority order (unchanged)

1. `BEADS_DIR/config.yaml` (highest)
2. Project `.beads/config.yaml`
3. `~/.config/bd/config.yaml` -- now works on macOS and Windows
4. `os.UserConfigDir()/bd/config.yaml` (platform default)
5. `~/.beads/config.yaml` (legacy, lowest)

### Files changed

- `internal/config/config.go` -- 19 lines added in `Initialize()`
- `internal/config/config_test.go` -- 3 new test functions
