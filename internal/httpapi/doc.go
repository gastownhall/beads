// Package httpapi implements the HTTP surface described by
// internal/httpapi/spec/openapi.v0.yaml — the /v0 wire contract `bd serve`
// answers on.
//
// What is live: the process lifecycle (Listen and Serve), the bind policy, the
// route table and the request path in front of it — Host allowlist, connection
// cap, database semaphore, per-request deadline, structured request log — the
// two operations that answer from the process itself, GET /healthz and
// GET /v0/beads/context, the three reads — GET /v0/beads/ready,
// GET /v0/beads/issues and GET /v0/beads/issues/{id} — and the one write,
// POST /v0/beads/issues/{id}:claim. ContextResponse.capabilities is derived
// from the implemented handlers, so a release cut mid-slice never advertises
// an operation that does not work.
//
// The reads hold no query logic of their own. Each decodes its parameters and
// hands the whole request to issueops.Reader, obtained from the provider's own
// capability accessor — the same role, reached the same way, that `bd show
// --json` reaches on a store. Filter construction, the workspace config it
// depends on, the default limits and the wisp fallback all live inside that
// role. A handler cannot WRITE a filter here, and that is machine-checked by
// two rules in .golangci.yml covering the two ways one gets written:
// httpapi-transport-boundary (depguard) denies internal/workapi from this
// package's non-test files, so the builders are not callable; the forbidigo
// rule denies naming types.IssueFilter or types.WorkFilter here at all, so a
// hand-rolled one is not writable either. The second is the one that
// matches the failure — the hosted viewer's bug was a bare IssueFilter, not a
// misused builder. What does stay here is transport — parameter decoding, the
// opaque cursor codec, the loopback-only refusal of an unlimited read, and the
// wire envelopes.
//
// The other front door is only PARTLY on the role, and the accurate statement
// of the anti-drift property is worth getting exactly right, because
// overstating it is what stops the next person checking.
//
// `bd show --json` is on the role, on both its routes. `bd ready` and `bd
// list` are not, on either: they consume the filter for modes the role does
// not express (see issueops.Reader's doc comment for the list, and for why
// splitting those commands would be worse). What they share instead is
// CONSTRUCTION — the same request types through the same builders, pinned by
// the builders' golden files — and, where it reaches, EXECUTION:
// workapi.FinishPage, the sort, trim and has-more verdict this surface's
// reader runs too, is `bd list`'s epilogue on both routes in every mode but
// the hierarchical --parent tree (which renders its recursive walk's result
// directly, on either route), and `bd ready`'s on the proxied route only.
// `bd ready`'s direct route keeps its own epilogue because it publishes a
// hidden-row TOTAL this surface has no member for.
//
// THE CLAIM, stated once and in full so it can be checked sentence by
// sentence. SHARED: all three issue reads on this surface go through
// issueops.Reader, and so does `bd show --json`'s detail view on both its
// routes; `bd list` and `bd ready` are not on the role and share instead the
// request types, the two builders in internal/workapi that their golden files
// pin, and workapi.FinishPage — `bd list` on both routes in every mode but the
// hierarchical --parent tree, `bd ready` on its proxied route only.
// ENFORCED, and by what: depguard (httpapi-transport-boundary) denies
// internal/workapi from every non-test file of this package, so no builder is
// callable here, and a forbidigo rule denies naming types.IssueFilter or
// types.WorkFilter here at all, so no filter is writable here either — both
// are directory-scoped with no per-file exception, so a file added to this
// package tomorrow is covered the moment it exists. That same forbidigo rule
// covers cmd/bd deny-by-default with 64 named exceptions, so the files
// implementing `bd list` and `bd show` cannot write a filter, and neither can
// a file they are split or renamed into unless the new name lands on that
// list. NOT ENFORCED: the rule forbids NAMING those types, not holding a
// value, so the property is "no filter is written here", not "every filter
// here came from a builder"; test files are exempt from both rules, because
// the oracles hold filters in order to inspect them; GET /healthz and GET
// /v0/beads/context answer from the process and its startup snapshot, are not
// issue queries, and are on no role; `bd ready`'s direct route and `bd list`'s
// hierarchical tree run epilogues of their own; and none of this is a merge
// gate — the rules run in `make ci-pr-lint` on every pull request and
// aggregate into the ci-gate job, but main carries no branch protection beyond
// deletion and non-fast-forward, so no check is GitHub-required and a red gate
// binds by convention.
//
// A THIRD rule closes the other way past the roles. Denying internal/workapi
// stops a handler building a filter; it says nothing about a handler taking
// uw.IssueUseCase() and calling the domain straight, which needs no filter and
// no builder — and which is what the proxied CLI did for years. depguard
// (httpapi-domain-boundary) therefore denies internal/storage/domain from this
// package's non-test files, deny-by-default so a file added tomorrow is covered
// the moment it exists. It has exactly two named holes, both for a VALUE the
// embedder assembled rather than a use case: server.go, whose Config.Workspace
// is the domain.ContextInfo startup snapshot it is handed, and handlers.go,
// whose contextResponse projects that snapshot through domain.PublishedContext
// — the same projection `bd context` publishes, and where the credential and
// host-path exclusions live. reads.go and claim.go carry no exception.
//
// The claim OPERATION is the only mutation this surface has, and claim.go
// states the two things a client must know before adopting it: the actor is
// caller-asserted provenance rather than authenticated identity, and hooks do
// not fire. It shares no function with `bd update --claim` above the
// transaction and deliberately so — the CAS itself is already one
// implementation one level down, in the domain's issue use case, which both
// reach. claimOnUOW says why nothing above that is worth extracting.
//
// The Host allowlist is the DNS-rebinding defense, and it has no off switch.
// Every bind answers to the loopback spellings and to the bound address itself;
// a WILDCARD bind (0.0.0.0, ::) has no single configured address, so it answers
// to any numeric IP literal and still refuses foreign DNS names. That last rule
// is the one place this deviates from the design's letter, which enumerated
// "the configured bind address" without saying what a wildcard means: a rebound
// page cannot produce an IP-literal Host, because the browser sends the hostname
// from the attacker's URL, so allowing literals keeps --allow-non-loopback
// usable on every interface while the defense survives — including on the
// serving host's own loopback, which is rebinding's canonical target. Matching
// is on parsed addresses, so every spelling of an allowed address is allowed.
//
// Around that sit the inert pieces the contract needs regardless: the
// problem+json error mapping, the shared limit defaults, and the compile-time
// pins that keep the generated types welded to the canonical structs.
//
// Two rules govern everything added here:
//
// The spec is the source of truth. Types come from it (`make api-gen`), never
// the other way round, and `make api-check` fails a change that edits one
// without the other.
//
// There is no wire struct. Responses marshal internal/types values directly,
// so the CLI's --json output and these bodies are one compatibility domain —
// which also means a serialized field on types.Issue cannot be renamed or
// removed without breaking the HTTP contract, not just the CLI's.
package httpapi
