# Backend Plugin Architecture Sketch

This branch makes backend construction reusable by the CLI and public Go API.

The first hook point is the configured store open path:

```text
metadata.json backend name -> configured factory -> built-in or trusted plugin -> storage.DoltStorage
```

Dolt remains the default backend. Future plugin-shaped backends should enter
through the same provider/process seam instead of adding backend-specific
branches to command code.

## Current Scope

- `internal/backend` defines provider registration, capabilities, and open
  options.
- Built-in providers register `dolt`, `postgres`, `mysql`, and `sqlite`.
- `cmd/bd/store_factory.go` routes configured backend opens through the
  provider registry.
- The public `beads.OpenConfigured` API uses the same configured factory and
  returns the core `Storage` plus a backend-neutral descriptor. The legacy
  `OpenFromConfig` and `OpenBestAvailable` entry points delegate to it.
- `metadata.json` preserves every non-empty backend name so the factory can
  resolve a built-in or external provider. Unknown names fail closed rather
  than silently opening Dolt. Metadata does not authorize an executable.
- Trusted plugin commands resolve from local-only sources: the
  `BEADS_BACKEND_PLUGIN_COMMAND` environment variable, `.beads/config.local.yaml`,
  or user-global config. This keeps clone-time metadata from becoming code
  execution when hooks run `bd` automatically.
- `backend/plugin` exposes the v1alpha1 process protocol and type aliases
  plugin authors need without importing Beads `internal/types`.
- `internal/backend/pluginprocess` can launch an external backend process,
  open a read/write or read-only session over the v1alpha1 newline-delimited JSON protocol, and expose
  the first storage methods needed by basic issue/config/ready-work flows.
- Command-scoped direct `dolt.Config` construction remains for the main CLI
  Dolt path because it carries auto-start, gateway credential, and proxy state.
  Configured helper opens and all non-Dolt command opens use the reusable
  factory.

## Plugin Implications

The registry is the in-process seam for every built-in backend. External
providers enter through the same selection path after local trust resolution.
DoltLite demonstrates that shape in an out-of-tree plugin:

```go
store, info, err := beads.OpenConfigured(ctx, beadsDir, beads.OpenConfiguredOptions{})
```

`info` reports the selected backend and optional behavior without exposing a
driver connection or concrete storage implementation.

```text
https://github.com/duncan4123/beads-backend-doltlite
```

Out-of-tree binaries can be installed with:

```bash
bd backend install doltlite --command /path/to/bd-backend-doltlite
```

That command records `backend = "doltlite"` in committed `.beads/metadata.json`
and writes the executable trust record to `.beads/config.local.yaml`. The local
file is gitignored by the canonical `.beads/.gitignore`; cloning a repository
with hostile `backend_plugin_command` metadata is therefore not sufficient to
make `bd` execute it.
