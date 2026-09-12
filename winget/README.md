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
- Copy to winget-pkgs: `manifests/g/GasTownHall/Beads/<version>/`

### SteveYegge.beads (legacy)

- `SteveYegge.beads.yaml` — version manifest
- `SteveYegge.beads.installer.yaml` — installer + PortableCommandAlias
- `SteveYegge.beads.locale.en-US.yaml` — locale
- Copy to winget-pkgs: `manifests/s/SteveYegge/beads/<version>/`

## Submitting to winget-pkgs

1. Fork https://github.com/microsoft/winget-pkgs
2. Place manifests under the paths above
3. Open a PR (or use `wingetcreate`)

```powershell
wingetcreate update GasTownHall.Beads --version <new-version> --urls <new-url> --submit
```

## Updating for new releases

```bash
./scripts/update-winget.sh <version>
```

This refreshes the SteveYegge.beads manifests (and regenerates GasTownHall.Beads.installer.yaml
with PortableCommandAlias). Then:

1. Update InstallerSha256 from the release `checksums.txt`
2. Commit, then PR to microsoft/winget-pkgs

### Getting the SHA256

```bash
curl -sL https://github.com/gastownhall/beads/releases/download/v<VERSION>/checksums.txt | grep windows
```
