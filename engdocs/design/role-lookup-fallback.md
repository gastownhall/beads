# Proposed role lookup fallback contract

Status: proposed for [#6826](https://github.com/gastownhall/beads/issues/6826).
This is a decision proposal for the `bd-7ilce` residual in the
[#6461 adoption review](https://github.com/gastownhall/beads/pull/6461#issuecomment-5854658042).
It changes no role behavior. The design-first hold remains until maintainers
choose the contract; an implementation would be separate work.

## Existing behavior and ownership

The inventory below describes #6461 head
`e33ff244e82d7fca92ff1c1657f785e068e3124f`, pending adoption into main. Its strict
role environment removes inherited routing and config suppression. That prevents
a caller from hiding a configured role through a null-config override; it does
not decide what to do when the resulting lookup is absent, invalid, or fails.

- `internal/routing.DetectUserRole` accepts a valid explicit role, retries the
  primary repository for a JJ secondary workspace, then warns and uses deprecated
  remote-URL heuristics. A missed role read is not always a Maintainer result:
  fork and plain HTTPS cases can select Contributor. Missing or unreadable origin
  can reach the local-project Maintainer default.
- The create and read auto-routing consumers currently log detection errors and
  continue. Returning a new error from the helper alone would not enforce a new
  caller outcome.
- The legacy init role getter collapses read failure and absence. The domain Git
  adapter distinguishes many read failures, but the proxied init tail ignores its
  role-read error and can write the default role when no value was returned.
- Doctor has a separate diagnostic path, including a legacy database fallback.
  It must not be treated as an authority-granting role resolver.

[#6384](https://github.com/gastownhall/beads/issues/6384) owns routing isolation,
[#6514](https://github.com/gastownhall/beads/issues/6514) owns writer targeting and
errors, and [#5243](https://github.com/gastownhall/beads/pull/5243) owns binding role
detection to the selected `bd -C` target. Preserve those implementations and
tests. This proposal does not widen the spelling of the role key, change remote
discovery, or redefine repository write permissions.

## Decision proposed

Separate a successfully observed absence from an unreadable or invalid role.
Retain the existing compatibility path for genuine absence; stop treating a
failed or invalid lookup as permission to select or persist a default role.

| Lookup result | Proposed consumer behavior |
| --- | --- |
| Valid explicit `maintainer` or `contributor` | Preserve that role and the selected-repository boundary. |
| Role genuinely absent, with usable repository/config discovery | Keep the existing warning and remote heuristic for auto-routing; keep init's existing default/prompt behavior. A confirmed local project with no remote keeps its Maintainer default. |
| Git unavailable, config unreadable/malformed, or lookup otherwise fails | Report the cause; auto-routing must not choose a role-dependent destination, and init must not persist an inferred role. Failure to read a remote must not stand in for proof that no remote exists. |
| Nonempty unrecognized role | Report the invalid value and the explicit configuration repair; do not silently substitute the URL heuristic or init default. |
| Inherited config suppression on a role operation | Remove it through #6461's strict boundary before lookup; suppression is not evidence of genuine absence. |

For role-dependent create and read operations, the proposed failure outcome is an
actionable error before acting on a selected role. An explicit `--repo` selection
that already bypasses role auto-routing stays separate. Doctor should report the
actual lookup failure or invalid value without turning its diagnostic fallback
into an inferred role. Explicit role-setting commands remain a repair path,
subject to their existing selected-repository and write-error contracts.

This choice preserves unconfigured-repository compatibility while making broken
configuration more visible. It can also interrupt workflows that currently
continue after a failed read, including a read-only auto-routed command. Keeping
today's fallback for every miss is a valid alternative if maintainers prioritize
that availability; requiring an explicit role even for genuine absence would
be a broader migration and is not proposed here.

## Acceptance after a decision

Before code work, agree on how each adapter distinguishes absence, empty values,
invalid values, and failures using actual Git results on supported hosts. Do not
infer that contract from a Boolean return or an exit code alone. Preserve the
JJ primary-workspace lookup and existing role precedence.

Exercise the real auto-routing, init, and doctor consumers with an absent role,
a valid role, a suppressed global role, an invalid value, and an unreadable or
malformed config. Use distinct target and decoy repositories. The cheapest
falsifiers are a read failure that still selects a role-dependent destination,
or a failed init role read that still writes the default. Also verify that a
genuinely unconfigured local repository retains the agreed compatibility path.

No classifier, role writer, or caller changes are included here. Maintainer
agreement on the failure outcomes and compatibility cost is still required.
