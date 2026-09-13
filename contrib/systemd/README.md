# A clock for due beads

Beads fire lazily, on ready-work reads. That is a latency *floor*, not a clock:
a workspace nobody reads never fires anything, so a deadline that arrives
overnight is silent until someone happens to run `bd ready`.

`bd due sweep` is the seam an external clock stands on. These units are a
worked example of one — a `systemd --user` timer on a fixed cadence:

```
bd-due-sweep.timer
  → bd-due-sweep.sh
       → bd due sweep --json     # fire what is due, reschedule, report
       → write last-report.json
       → write last-sweep        # heartbeat, written last
```

Nothing here is required to use due dates, and beads knows nothing about these
files. Use them, adapt them, or drive `bd due sweep` from cron, a CI schedule,
or your own supervisor instead.

## Install

```sh
mkdir -p ~/.local/libexec ~/.config/systemd/user
install -m 0755 bd-due-sweep.sh ~/.local/libexec/
install -m 0644 bd-due-sweep.service bd-due-sweep.timer \
                bd-due-sweep-failure.service ~/.config/systemd/user/

systemctl --user edit bd-due-sweep.service   # set the workspace
loginctl enable-linger "$USER"               # so the timer runs while logged out
systemctl --user daemon-reload
systemctl --user enable --now bd-due-sweep.timer
```

The workspace is the one setting with no sensible default. Set it in a drop-in
rather than editing the shipped unit:

```ini
[Service]
Environment=BD_DUE_SWEEP_WORKSPACE=/path/to/workspace
Environment=BD_DUE_SWEEP_BD=%h/.local/bin/bd
```

| Variable | Default | Meaning |
|---|---|---|
| `BD_DUE_SWEEP_WORKSPACE` | *(required)* | Directory to sweep |
| `BD_DUE_SWEEP_BD` | `bd` | The `bd` binary to run |
| `BD_DUE_SWEEP_TIMEOUT` | `120` | Seconds before the sweep is killed |
| `BD_DUE_SWEEP_STATE` | `$XDG_STATE_HOME/bd-due-sweep` | Lock, heartbeat, last report |
| `BD_DUE_SWEEP_PUBLISH` | *(none)* | Hook run as `"$PUBLISH" "<summary>" "<report.json>"` |

Sweeping several workspaces is one instance per workspace: copy the unit pair
under a second name with its own `BD_DUE_SWEEP_WORKSPACE` and
`BD_DUE_SWEEP_STATE`.

## Watching the clock

The sweep exits non-zero when it could not run, so a quiet workspace is
distinguishable from a broken one. Two things make that visible:

- `OnFailure=bd-due-sweep-failure.service` turns a hard failure into a logged,
  loud event. Replace its `ExecStart` with whatever your site already pages on.
- `last-sweep`'s mtime is *when the clock last completed*. Alert on it going
  stale **from another host** — a timer cannot report its own death.

```sh
systemctl --user list-timers bd-due-sweep.timer
journalctl --user -u bd-due-sweep.service -n 20
cat "${XDG_STATE_HOME:-$HOME/.local/state}/bd-due-sweep/last-sweep"
```
