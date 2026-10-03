# Proposed Git null-config spelling contract

Status: proposed for [#6827](https://github.com/gastownhall/beads/issues/6827).
This records a choice for the `bd-7ilce` residual in the
[#6461 adoption review](https://github.com/gastownhall/beads/pull/6461#issuecomment-5854658042).
It changes no classifier or caller. The design-first hold remains until
maintainers choose the supported spelling contract.

## Decision proposed

Keep #6461's narrow lexical recognition: an exact `/dev/null` value, or a
case-insensitive `NUL` value on Windows, can retain system/global config
suppression. Do not resolve filesystem aliases, clean paths, trim whitespace,
or generalize Windows device syntax in this classifier. This is a proposed
supported-input contract, not a claim that other spellings cannot name a null
device on some host.

The following table describes `internal/gitenv` at #6461 head
`e33ff244e82d7fca92ff1c1657f785e068e3124f`, pending adoption into main. It covers
`GIT_CONFIG_GLOBAL` and `GIT_CONFIG_SYSTEM` values after the existing host-aware
environment-key matching; it does not change that matching.

| Value | Ordinary routing scrub | Strict role-authority scrub |
| --- | --- | --- |
| Exact `/dev/null` | Retained on all hosts | Removed |
| `NUL`, `nul`, or another case variant | Retained on Windows; removed on POSIX | Removed |
| A different spelling, including `/dev/./null`, `NUL:`, a device-namespace path, or a symlink | Removed | Removed |
| An arbitrary file path, an empty assignment, or an unassigned entry | Removed | Removed |

Removal means Git can resume its ordinary config discovery; it does not mean
that the variable has disabled config. A harness requiring suppression should
use a recognized spelling and verify the behavior of its actual Git host.

`GIT_CONFIG_NOSYSTEM` is a separate, assigned, value-blind control in #6461: its
value cannot select a file, and Git interprets its Boolean value or reports an
invalid one. This proposal leaves that behavior unchanged. It also leaves
arbitrary config paths and inline `GIT_CONFIG_*` entries without routing
authority.

## Compatibility and alternatives

Retaining the lexical boundary avoids a new filesystem dependency and preserves
the reviewed distinction between suppression and caller-selected configuration.
The cost is that a harness using another null-device spelling can unexpectedly
read ordinary global/system config after scrubbing. Documenting the accepted
spellings makes that limit explicit; the current review did not require an
expansion.

An alternative is to add specific host-supported spellings. That decision needs
an explicit list and native-host evidence that each spelling cannot select a
caller-controlled config file. It must state how device namespaces, relative
paths, filesystem aliases, and replacement between checking and use affect the
claim. A one-time filesystem identity check is not by itself a contract for a
later Git open. Do not treat arbitrary empty files as null-device suppression.

## Acceptance after a decision

If the narrow policy is accepted, document the spellings with #6461's adopted
implementation and close the decision without widening the classifier. Retain
the Windows/POSIX table, source-slice ownership, order, duplicates, and unrelated
environment controls already covered by the shared helpers.

If an expansion is chosen, first demonstrate each added spelling with real Git
on the claimed host, a global/system config containing a distinguishing value,
and a caller-controlled decoy path. The falsifier is either a supposedly
suppressing value that reads the decoy, or a supported spelling that fails to
suppress config. In either outcome, role readers and writers must continue to
remove suppression through `ScrubRoutingAndSuppression`.

The role fallback decision in
[#6826](https://github.com/gastownhall/beads/issues/6826), identity behavior, and
worktree discovery policy remain separate. No runtime change is authorized by
this proposed contract alone.
