# Guarded JSON batch CLI input

`bd batch --input-format=json --file request.json --json` projects one bounded,
schema-versioned request onto the existing public `issueops.BatchApplier` role.
The legacy line grammar remains the default. This adds no orchestration policy,
new storage schema, or alternative transaction implementation.

The envelope is `{"schema_version":"1","request":{...}}`. Operation fields retain
the exported Go names of `issueops.ApplyBatchRequest`, `ApplyItem` and their
patch types; the nested Issue retains its existing lowercase JSON tags. For
example:

```json
{
  "schema_version": "1",
  "request": {
    "Provenance": "retained evidence publication",
    "Items": [
      {
        "Kind": "create",
        "Create": {
          "Issue": {
            "id": "bd-evidence",
            "title": "Retained evidence",
            "issue_type": "task",
            "status": "closed",
            "description": "Opaque source body"
          }
        }
      }
    ]
  }
}
```

`Request.Actor` must be empty. The CLI supplies its existing actor provenance;
this does not turn an actor string into authenticated identity. Use
`Request.Provenance` instead of `--message`. JSON mode refuses `--dry-run`,
unknown fields, multiple objects, unsupported schema versions and inputs larger
than 8 MiB before publication. Item validation and limits belong to the SDK's
existing batch planner.

`ExpectedVersion` carries the provider's equality-only row-lock token. Its
coverage is partial; it is not a complete content version, timestamp or Dolt
commit. Consumers needing exact evidence must retain the observed body and
recheck currentness separately. Typed metadata has existing SDK/storage JSON
semantics; this transport does not repair numeric precision in every storage
or output path. Opaque descriptions preserve evidence without interpreting its
numbers. Callers must retain int64 guards without a JavaScript float conversion.

Both direct and proxied modes call the existing owning BatchApplier. A failed
guard rolls back a preceding pinned create, and an occupied pinned identity
refuses the batch. The embedded and isolated managed-proxy fixtures exercise
that boundary, including closed creates, typed metadata and opaque evidence
readback. The required PR CI job `Test (Dolt server fingerprint)` also runs the shared
container, embedded and managed-proxy parity rows with
`BEADS_TEST_REQUIRE_DOLT_CONTAINER=1` and `BEADS_TEST_PROXIED_SERVER=1`.
Unavailable container infrastructure fails this check instead of skipping it.
The two proxied rows are additionally in the generated 30-shard Bazel manifest
and its required `BAZEL_PROXIED` gate.

This CLI mode does not claim BDP conformance or introduce application record,
link, authority, routing or scheduling meanings. Those belong to consumers.
The published generated CLI docs remain pinned to the latest released bd tag;
the new source help will enter that documentation at the next release-pin bump.
