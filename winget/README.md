# Windows Package Manager (winget) Manifests

This directory holds winget manifests for publishing beads to the Windows Package Manager.

## Package identifiers

| Identifier | Install command | Notes |
|---|---|---|
| **GasTownHall.Beads** | `winget install GasTownHall.Beads` | Current community package (v1.x). Prefer this for new installs. |
| **SteveYegge.beads** | `winget install SteveYegge.beads` | Legacy identifier (0.30.x era). Kept for continuity. |

Both installer manifests **must** set `PortableCommandAlias: bd` under `NestedInstallerFiles`.
That is what creates `%LOCALAPPDATA%\Microsoft\WinGet\Links\bd.exe`.

`Commands: [bd]` alone is **search metadata only** — it does not create a symlink.
Without `PortableCommandAlias`, winget only adds the package folder to PATH (inherited by
*new* processes). Already-running shells, editors, and agents never see `bd` until restart
(GH#4908).

## Manifest files

### GasTownHall.Beads (current)

- `GasTownHall.Beads.installer.yaml` — installer + **PortableCommandAlias**

This repo carries only the installer manifest for this id, but a winget-pkgs version
directory needs a `version` + `defaultLocale` + `installer` set. **Publish this id with
`wingetcreate update`** (below), which supplies the other two from the already-published
manifests. Hand-copying just this file produces a set winget-pkgs validation rejects.

### SteveYegge.beads (legacy)

- `SteveYegge.beads.yaml` — version manifest
- `SteveYegge.beads.installer.yaml` — installer + PortableCommandAlias
- `SteveYegge.beads.locale.en-US.yaml` — locale
- Copy to winget-pkgs: `manifests/s/SteveYegge/beads/<version>/`

The hand-copy path applies to this id only — it is the one with a complete three-file set.

## Submitting to winget-pkgs

1. Fork https://github.com/microsoft/winget-pkgs
2. For `SteveYegge.beads`, place the three manifests under the path above
3. For `GasTownHall.Beads`, use `wingetcreate` rather than a hand-copied directory
4. Open a PR

```powershell
wingetcreate update GasTownHall.Beads --version <new-version> --urls <new-url> --submit
```

## Updating for new releases

```bash
./scripts/update-winget.sh <version>
```

This refreshes the SteveYegge.beads manifests (and regenerates GasTownHall.Beads.installer.yaml
with PortableCommandAlias). Note that it rewrites **both** ids to `<version>`, including the
legacy one — so the first run moves `SteveYegge.beads` off the 0.30.x version named above.
Then:

1. Update InstallerSha256 from the release `checksums.txt`
2. Commit, then PR to microsoft/winget-pkgs

### Getting the SHA256

```bash
curl -sL https://github.com/gastownhall/beads/releases/download/v<VERSION>/checksums.txt | grep windows_
```

The `windows_amd64` line is the `x64` `InstallerSha256`; the `windows_arm64` line is the
`arm64` one. A bare `grep windows` matches both and is what produced the two-hash
`InstallerSha256` that `scripts/check-winget-portable-alias.sh` now catches.
