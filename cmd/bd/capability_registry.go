package main

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The proxied-server capability registry: one table for every command path.
//
// Before this file the policy lived in three separate tables — a maintenance
// refusal map, a history classification map, and a transform row list — each
// with its own lookup and its own front-door validator. Nothing forced a new
// command to appear in any of them, so an unclassified command sailed past the
// gate and died deep in the store factory with an untyped error. The fix is not
// a fourth table: it is one table plus a test that fails when a command is
// missing from it (TestProxyCapabilityRegistryCoversCommandTree).
//
// WHAT A ROW MEANS. Outcome describes what the PRE-PROVIDER GATE does with the
// path on a proxied-server workspace, nothing more:
//
//	ProxyOutcomeRefused        the gate refuses with a typed ProxyCapabilityError
//	                           before any workspace side effect
//	ProxyOutcomeRefusedInRunE  the gate lets the path through and the command
//	                           refuses inside its own RunE with an untyped
//	                           string; recorded here so the untyped surface is
//	                           inventoried rather than invisible. Not enforced.
//	ProxyOutcomeHonored        the gate lets the path through. It does NOT
//	                           promise a proxied route exists: a permitted
//	                           command with no route still fails at the store
//	                           factory with proxy.store.unrouted.
//
// Permitting a path also COSTS something, and the cost is invisible from this
// table: unless the command is on one of main.go's store-init skip lists, the
// root pre-run opens the proxied provider before RunE runs — which starts the
// proxy and its dolt child, creates the database and migrates the schema. For a
// command that genuinely uses the store that is the point. For a store-free one
// it is pure side effect, and on a workspace whose topology cannot be resolved
// it turns a clean typed refusal into a provider-open error. So "this command
// touches no store" is an argument for permitting it only once the command is
// also off the store-init path; see the `migrate hooks` row. Nothing here
// asserts that, and TestProxyCapabilityRegistryCoversCommandTree cannot see it.
//
// REASON is the load-bearing field. "design" means refusing is correct and is
// expected to stay that way — the semantics are undefined or unsafe against a
// backend that may be shared. "unimplemented" means the refusal is a capability
// gap with a named slice that closes it, and Tracking says which. Every later
// slice measures itself by turning "unimplemented" rows into honored ones; the
// count of remaining "unimplemented" rows is the completion signal for the whole
// proxied-parity effort. Assign the field row by row, from the command's actual
// semantics — a bulk assignment would destroy the only information it carries.
//
// The reason also renders in the refusal JSON, additive to the frozen
// {code, error, mutates} contract (see buildJSONCapabilityError). Codes are
// frozen; messages are informative-only.

// ProxyRefusalReason distinguishes a deliberate policy refusal from a
// not-yet-built one.
type ProxyRefusalReason string

const (
	// ProxyReasonDesign: the command is refused on purpose and is expected to
	// stay refused. Lifting it would need a semantic decision, not plumbing.
	ProxyReasonDesign ProxyRefusalReason = "design"
	// ProxyReasonUnimplemented: the command could work over the proxied
	// provider; nobody has routed it yet. Tracking names the work item.
	ProxyReasonUnimplemented ProxyRefusalReason = "unimplemented"
)

// Tracking values name the plan-of-record work item for an unimplemented
// refusal. These are the slices of the proxied-parity plan; a row gets a GitHub
// issue or bead ID instead once one is filed for it.
// S3 has no entry: it landed, and its rows carry Reason=design rather than a
// tracking item.
const (
	trackVersionCtl  = "proxied-parity S4 (dolt commit/push/pull/remote)"
	trackSync        = "proxied-parity S5 (bd sync)"
	trackDegradation = "proxied-parity S6 (doctor, config show, degradations)"
	trackLongTail    = "proxied-parity S7 (long-tail policy pass)"
)

// capabilityRow is one command-path policy row. ArgSet is the sorted set of
// execution-altering flags that were set (for example "--auto-merge --dry-run"),
// empty for the bare path; a row keyed on an ArgSet wins over the bare row, which
// is how `duplicates --auto-merge --dry-run` stays honored while
// `duplicates --auto-merge` is refused.
//
// Topology overrides Rule on the proxied shapes named in it and is empty for
// every row whose answer is the same everywhere — which is all of them but the
// backup family. See honoredOn.
type capabilityRow struct {
	Path     string
	ArgSet   string
	Rule     proxyCapabilityRule
	Topology map[ProxyTopology]proxyCapabilityRule
	History  HistoryCapabilityClass
	Note     string
}

// ruleFor resolves the row's policy on one proxied topology.
func (r capabilityRow) ruleFor(topology ProxyTopology) proxyCapabilityRule {
	if rule, ok := r.Topology[topology]; ok {
		return rule
	}
	return r.Rule
}

// honoredOn narrows a refusal to the shapes where it is actually true: the row
// goes on refusing everywhere except the topologies named here.
//
// The direction is deliberate. A row states its refusal first and then lists
// the exceptions, so a shape nobody thought about — a new transport, or the
// unknown a corrupt sidecar produces — inherits the refusal rather than the
// exemption.
func (r capabilityRow) honoredOn(topologies ...ProxyTopology) capabilityRow {
	r.Topology = make(map[ProxyTopology]proxyCapabilityRule, len(topologies))
	for _, topology := range topologies {
		r.Topology[topology] = proxyCapabilityRule{Outcome: ProxyOutcomeHonored}
	}
	return r
}

// display renders the row's key the way its refusal message names it.
func (r capabilityRow) display() string {
	if r.ArgSet == "" {
		return r.Path
	}
	return r.Path + " " + r.ArgSet
}

func (r capabilityRow) withHistory(class HistoryCapabilityClass) capabilityRow {
	r.History = class
	return r
}

// asParentGroup marks a row whose path is a command GROUP. Cobra answers a
// non-runnable parent with its help text before PersistentPreRunE, so the gate
// never sees it; the row is kept because it is the parent every child row
// inherits its code from, and because a parent that later gains a Run must not
// silently become permitted.
func (r capabilityRow) asParentGroup() capabilityRow {
	r.Note = "parent group: cobra shows help instead of reaching the gate"
	return r
}

func (r capabilityRow) withMessage(message string) capabilityRow {
	r.Rule.Message = message
	return r
}

// refusedPath builds a typed front-door refusal for a bare command path.
func refusedPath(path, code string, reason ProxyRefusalReason, tracking string) capabilityRow {
	return refusedArgs(path, "", code, reason, tracking)
}

// refusedArgs builds a typed front-door refusal keyed on a flag set.
func refusedArgs(path, argSet, code string, reason ProxyRefusalReason, tracking string) capabilityRow {
	row := capabilityRow{Path: path, ArgSet: argSet}
	row.Rule = proxyCapabilityRule{
		Outcome:  ProxyOutcomeRefused,
		Code:     code,
		Message:  row.display() + " is not supported in proxied-server mode",
		ExitCode: 1,
		Reason:   reason,
		Tracking: tracking,
	}
	return row
}

// refusedInRunE records a command that the gate permits and that then refuses
// itself with an untyped string from RunE. The row carries no message: the
// string lives in the command, and duplicating it here would rot. The scan in
// TestProxyCapabilityRegistryCoversInlineRefusals keeps the two in step.
//
// The reason is always "unimplemented" and the owner is always the long-tail
// slice, because an untyped string inside RunE is not a considered policy
// statement: a refusal decided on purpose belongs in the gate, typed, where a
// consumer can read it. Anything here is a family nobody has routed yet.
func refusedInRunE(path, site string) capabilityRow {
	return capabilityRow{
		Path: path,
		Rule: proxyCapabilityRule{
			Outcome: ProxyOutcomeRefusedInRunE, ExitCode: 1,
			Reason: ProxyReasonUnimplemented, Tracking: trackLongTail,
		},
		Note: "untyped refusal in " + site,
	}
}

// permitted records that the gate lets a path through. See the header: this is
// a statement about the gate, not a promise that a proxied route exists.
func permitted(path string) capabilityRow {
	return capabilityRow{Path: path, Rule: proxyCapabilityRule{Outcome: ProxyOutcomeHonored}}
}

// backupRefusal builds one backup-family row: honored where bd owns the Dolt
// server, refused by design everywhere else. The code stays
// proxy.backup.unsupported — consumers have been keying on it since 1.3.0 and
// the refusal it names has not changed, only its reason and its scope — and the
// message keeps the frozen "<path> is not supported in proxied-server mode"
// prefix, with the reason appended.
//
// The message leads with SERVER-GLOBAL STATE rather than with the filesystem,
// because only the filesystem half is scheme-specific. `bd backup init` accepts
// https/aws/gs as well as file:// (resolveDoltBackupURL) and the help text
// recommends DoltHub; for those a server-side push over the network with the
// server's own credentials is a coherent thing to ask for, and "the destination
// is on the server's disk" says nothing about it. What is true of every scheme
// is that `CALL DOLT_BACKUP('add', …)` registers the remote ON THE SERVER,
// where it is global to every client of that server — one workspace's backup
// decision silently becomes everyone's, which is the shape of the auto-backup
// storm this command's own help text warns about.
//
// backupRemoteSchemeTracking is why these rows carry a Tracking item despite
// being Reason=design: the outcome is settled, the remote-scheme CASE is not.
func backupRefusal(path string) capabilityRow {
	return refusedPath(path, "proxy.backup.unsupported", ProxyReasonDesign, backupRemoteSchemeTracking).
		withMessage(path + " is not supported in proxied-server mode against a Dolt server bd does not own: " +
			"the backup remote is registered on the server, where it is global to every client, " +
			"and a file:// destination names a filesystem this client cannot see").
		honoredOn(ProxyTopologyManagedLocal)
}

// backupRemoteSchemeTracking records the one part of the backup refusal that is
// a question rather than a verdict, so refusing the family off-host does not
// silently foreclose it. A remote-scheme destination (DoltHub, aws://, gs://)
// needs no filesystem bd can see — the server pushes it over the network from
// its own environment, which is exactly the credential story S4 has to settle
// for `dolt push` on these same topologies. Whether a per-client backup remote
// on a shared server is ever acceptable is the open part; the plumbing is not.
//
// Note also that the DoltHub story is already half-built in DIRECT mode:
// `bd backup init https://…` and `bd backup sync` work, but
// `bd backup restore https://…` does not, because validateBackupRestoreDir
// os.Stats its argument and rejects anything that is not an existing directory
// on every topology. Pre-existing, not something this slice changed.
const backupRemoteSchemeTracking = "open question, not a verdict: remote-scheme (DoltHub/aws/gs) backup on a server " +
	"bd does not own — revisit with S4, which settles the same server-env credential question for dolt push"

// proxyCapabilityRegistry is the whole path-keyed policy. Rows for permitted
// paths are appended from proxyPermittedPaths by init.
var proxyCapabilityRegistry = []capabilityRow{
	// --- doctor -------------------------------------------------------------
	// Diagnosis needs no version-control surface; the checks simply have no
	// proxied dual yet. S6 splits them into client-local and store-backed.
	refusedPath("doctor", "proxy.doctor.unsupported", ProxyReasonUnimplemented, trackDegradation),

	// --- backup / restore ---------------------------------------------------
	// CALL DOLT_BACKUP(...) is pure SQL over a connection proxied mode already
	// owns, so routing this family needed no proxy work at all — see
	// backup_proxied_server.go.
	//
	// What it did need is the ownership policy, which is the first genuinely
	// topology-differentiated rule in this table. A backup remote belongs to the
	// SERVER, not to the client that asked for it: `CALL DOLT_BACKUP('add', …)`
	// registers it in the server's own configuration, where it is global to
	// every client connected to that server. On managed-local "every client" is
	// this workspace, because bd spawned the child itself. Anywhere else one
	// operator's backup decision silently becomes everyone's — the same shape as
	// the auto-backup storm this command's help text already warns about, and
	// the reason team-server is the sharpest case: bts owns the store.
	//
	// A file:// destination adds a second, scheme-specific problem on top: it
	// names a path on the machine running dolt, so off-host the command either
	// fails at the point of use or, far worse, reports success having written
	// the backup somewhere the operator will never look for it.
	//
	// Hence: honored on managed-local, refused by design everywhere else. The
	// OUTCOME is design — server-global state is not something plumbing on this
	// side can make per-client, and a backup story for those shapes belongs to
	// whoever runs the server. The remote-scheme case (DoltHub, aws://, gs://,
	// which resolveDoltBackupURL accepts and backup.go recommends) is the part
	// that is a question rather than a verdict, and it is not silently
	// foreclosed: see backupRemoteSchemeTracking, which every row below carries.
	backupRefusal("backup").asParentGroup(),
	backupRefusal("backup init"),
	backupRefusal("backup sync"),
	backupRefusal("backup remove"),
	backupRefusal("backup status"),
	backupRefusal("backup restore"),

	// --- version control over a shared history ------------------------------
	// Refused by design: these mutate or traverse a history that several
	// clients share through one connection, and the per-client semantics
	// (whose working set? whose branch head?) are undefined. `diff` is the one
	// plausible 1.4 candidate, via the dolt_diff system tables.
	refusedPath("branch", "proxy.branch.unsupported", ProxyReasonDesign, "").
		withHistory(HistoryDirectOnly),
	refusedPath("diff", "proxy.diff.unsupported", ProxyReasonDesign, "1.4 candidate: dolt_diff system tables"),
	refusedPath("flatten", "proxy.flatten.unsupported", ProxyReasonDesign, "").
		withHistory(HistoryDirectOnly),
	refusedPath("conflicts", "proxy.conflicts.unsupported", ProxyReasonDesign, "").
		withHistory(HistoryDirectOnly).
		asParentGroup(),
	refusedPath("conflicts list", "proxy.conflicts.unsupported", ProxyReasonDesign, ""),
	refusedPath("conflicts show", "proxy.conflicts.unsupported", ProxyReasonDesign, ""),
	refusedPath("conflicts resolve", "proxy.conflicts.unsupported", ProxyReasonDesign, ""),
	refusedPath("vc", "proxy.vc.unsupported", ProxyReasonDesign, "").
		withHistory(HistoryDirectOnly).
		asParentGroup(),
	refusedPath("vc merge", "proxy.vc.unsupported", ProxyReasonDesign, ""),
	refusedPath("vc commit", "proxy.vc.unsupported", ProxyReasonDesign, ""),
	refusedPath("vc status", "proxy.vc.unsupported", ProxyReasonDesign, ""),
	refusedPath("federation", "proxy.federation.unsupported", ProxyReasonDesign, "").
		withHistory(HistoryDirectOnly).
		asParentGroup(),
	refusedPath("federation sync", "proxy.federation.unsupported", ProxyReasonDesign, ""),
	refusedPath("federation status", "proxy.federation.unsupported", ProxyReasonDesign, ""),
	refusedPath("federation add-peer", "proxy.federation.unsupported", ProxyReasonDesign, ""),
	refusedPath("federation remove-peer", "proxy.federation.unsupported", ProxyReasonDesign, ""),
	refusedPath("federation list-peers", "proxy.federation.unsupported", ProxyReasonDesign, ""),

	// --- multi-repo routing -------------------------------------------------
	// Routing to another workspace bypasses the proxied root entirely, which is
	// the same reason --repo is N/A mode-wide.
	refusedPath("repo", "proxy.repo.unsupported", ProxyReasonDesign, "").
		withHistory(HistoryDirectOnly).
		asParentGroup(),
	refusedPath("repo add", "proxy.repo.unsupported", ProxyReasonDesign, ""),
	refusedPath("repo remove", "proxy.repo.unsupported", ProxyReasonDesign, ""),
	refusedPath("repo list", "proxy.repo.unsupported", ProxyReasonDesign, ""),
	refusedPath("repo sync", "proxy.repo.unsupported", ProxyReasonDesign, ""),

	// --- dolt version-control verbs -----------------------------------------
	// CALL DOLT_PUSH/DOLT_PULL/DOLT_COMMIT are SQL procedures and the generic
	// repository that wraps them is already written; these need a use case and
	// a route, not proxy work. `dolt remote remove` is already routed, which is
	// the precedent.
	refusedPath("dolt commit", "proxy.dolt_commit.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly),
	refusedPath("dolt push", "proxy.dolt_push.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly),
	refusedPath("dolt pull", "proxy.dolt_pull.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly),
	refusedPath("dolt remote", "proxy.dolt_remote.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly).
		asParentGroup(),
	refusedPath("dolt remote add", "proxy.dolt_remote.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly),
	refusedPath("dolt remote list", "proxy.dolt_remote.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly),
	refusedPath("dolt remote reset-data", "proxy.dolt_remote.unsupported", ProxyReasonUnimplemented, trackVersionCtl).
		withHistory(HistoryDirectOnly),
	refusedPath("sync", "proxy.sync.unsupported", ProxyReasonUnimplemented, trackSync).
		withHistory(HistoryDirectOnly),

	// --- migration and workspace surgery ------------------------------------
	// Storage-format and workspace surgery against a store other clients may be
	// holding open. `migrate schema` is the exception and is already routed:
	// the provider open runs it under the migration gate.
	//
	// S7 MUST RE-EXAMINE THE ROWS BELOW PER TOPOLOGY. "A store other clients may
	// be holding open" describes external and team-server workspaces; it does
	// not describe managed-local, which is 1:1 and bd-owned — bd spawned the
	// dolt child itself. A row cannot yet say "refused, except on managed-local"
	// (S3's backup locality policy is the slice that adds per-topology rules),
	// so the blanket refusal is the only thing this table can express today.
	// These rows keep it because nobody has done the semantic work to narrow
	// them, not because the work has been done and come out this way.
	refusedPath("migrate", "proxy.migrate.unsupported", ProxyReasonDesign, ""),
	refusedPath("migrate sync", "proxy.migrate.unsupported", ProxyReasonDesign, ""),
	// `migrate hooks` is the one row in this block the surgery rationale never
	// described: it plans and applies GIT HOOK FILE migration (migrate_hooks.go
	// -> doctor.PlanHookMigration) on the local filesystem and opens no store at
	// all. It is refused for a structural reason instead — the command is wired
	// into the store-opening path it does not need. It is on none of main.go's
	// store-init skip lists, so permitting it makes the root pre-run open the
	// proxied provider before RunE ever runs. Measured, not assumed: with this
	// row flipped to permitted, `bd migrate hooks --dry-run` on a managed-local
	// workspace started the proxy AND its dolt child (both pidfiles present) to
	// print a hook plan, and on a workspace with an unreadable sidecar it exited
	// 1 with "failed to open uow provider: corrupt proxied-server sidecar ..."
	// where it had cleanly refused before. Trading a typed refusal for a
	// connection error is not a lift. The honest fix is to take the command off
	// the store path first, which is S7's job, not this slice's.
	refusedPath("migrate hooks", "proxy.migrate.unsupported", ProxyReasonUnimplemented,
		trackLongTail+" (specifically: put `migrate hooks` on main.go's store-init skip list, then permit it)"),
	refusedPath("migrate issues", "proxy.migrate.unsupported", ProxyReasonDesign, ""),
	refusedPath("migrate-issues", "proxy.migrate.unsupported", ProxyReasonDesign, ""),
	refusedPath("migrate-personal", "proxy.migrate.unsupported", ProxyReasonDesign, ""),

	// --- destructive admin --------------------------------------------------
	// Same topology-blind rationale as the migrate block above, and the same
	// instruction to S7: "destructive against a possibly-shared store" is a
	// claim about external and team-server shapes. On managed-local the store is
	// this workspace's alone, and `bd admin reset` there is no more shared than
	// it is in embedded mode, where it is honored. Re-examine per topology.
	refusedPath("admin cleanup", "proxy.admin.unsupported", ProxyReasonDesign, ""),
	refusedPath("admin reset", "proxy.admin.unsupported", ProxyReasonDesign, ""),
	// --- issue compaction ---------------------------------------------------
	// `bd admin compact` without --dolt is the description-rewriting pass; only
	// the --dolt arm has a proxied route (runCompactDoltProxiedServer). The
	// --dolt arm is decided by flag VALUE, so the gate keeps a value-aware
	// branch for it and lands here when the flag is off.
	refusedPath("admin compact", "proxy.compact.unsupported", ProxyReasonUnimplemented, trackLongTail).
		withMessage("only 'compact --dolt' is supported in proxied-server mode"),
	// `bd restore` recovers a COMPACTED ISSUE's original text, not a backup —
	// the backup verb is `bd backup restore`. Its snapshot read is ordinary
	// CRUD; only its fallback path (reconstructing from Dolt history when no
	// snapshot was archived) touches version control.
	refusedPath("restore", "proxy.restore.unsupported", ProxyReasonUnimplemented, trackLongTail),

	// --- gates, formulas, swarm, merge slots --------------------------------
	// Ordinary CRUD over a store with no version-control surface. They stay
	// refused because nothing downstream has asked for them, which is this
	// codebase's standing rule for lifting a refusal — not because the proxied
	// topology makes them wrong.
	refusedPath("gate discover", "proxy.gate.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("cook", "proxy.formula.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("ship", "proxy.formula.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("swarm create", "proxy.swarm.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("swarm list", "proxy.swarm.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("merge-slot create", "proxy.merge_slot.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("merge-slot check", "proxy.merge_slot.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("merge-slot acquire", "proxy.merge_slot.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("merge-slot release", "proxy.merge_slot.unsupported", ProxyReasonUnimplemented, trackLongTail),

	// --- transforms ---------------------------------------------------------
	// ID rewrites walk every reference in the store. They need a routed
	// multi-statement transaction, which the UOW can express; nobody has
	// written it. The arg-set rows are what let the read-only preview of a
	// duplicate merge stay honored while the merge itself is refused.
	refusedPath("rename", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("rename-prefix", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedArgs("rename-prefix", "--dry-run", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedArgs("rename-prefix", "--repair", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedArgs("rename-prefix", "--dry-run --repair", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("duplicate", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedArgs("duplicate", "--of", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedPath("supersede", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedArgs("supersede", "--with", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	refusedArgs("duplicates", "--auto-merge", "proxy.transform.unsupported", ProxyReasonUnimplemented, trackLongTail),
	// The dry run reports what the merge WOULD do without writing anything, so
	// it rides the read path plain `duplicates` already uses.
	{Path: "duplicates", ArgSet: "--auto-merge --dry-run", Rule: proxyCapabilityRule{Outcome: ProxyOutcomeHonored}},

	// --- routed history -----------------------------------------------------
	// The two paths that already have a proxied route, recorded so the class is
	// asserted rather than remembered.
	permitted("history").withHistory(HistoryProxySupported),
	permitted("dolt remote remove").withHistory(HistoryProxySupported),

	// --- untyped refusals inside RunE ---------------------------------------
	// The gate permits these paths and the command refuses itself with a bare
	// string, so a JSON consumer gets no code and no mutates flag. They are
	// listed to inventory that surface, not to enforce it: converting them to
	// typed refusals changes observable output and belongs to the slice that
	// owns each family. Tracker verbs are ordinary store reads plus a network
	// call; nothing about the proxied topology makes them wrong.
	refusedInRunE("ado status", "ado.go"),
	refusedInRunE("ado projects", "ado.go"),
	refusedInRunE("ado sync", "ado.go"),
	refusedInRunE("github status", "github.go"),
	refusedInRunE("github repos", "github.go"),
	refusedInRunE("github sync", "github.go"),
	refusedInRunE("gitlab status", "gitlab.go"),
	refusedInRunE("gitlab projects", "gitlab.go"),
	refusedInRunE("gitlab sync", "gitlab.go"),
	refusedInRunE("notion status", "notion.go"),
	refusedInRunE("notion init", "notion.go"),
	refusedInRunE("notion connect", "notion.go"),
	refusedInRunE("notion sync", "notion.go"),
	refusedInRunE("create-form", "create_form.go"),
}

// proxyPermittedPaths lists every command path the proxied front door lets
// through. It exists so that a command added without a policy decision fails
// TestProxyCapabilityRegistryCoversCommandTree instead of shipping — the whole
// point of the registry. Being listed here says the GATE permits the path; a
// permitted command with no proxied route still fails at the store factory with
// proxy.store.unrouted.
var proxyPermittedPaths = []string{
	// issue CRUD and queries
	"assign", "batch", "blocked", "children", "close", "comment", "count", "create",
	"defer", "delete", "duplicates", "edit", "export", "find-duplicates", "forget",
	"gc", "graph", "graph check", "heartbeat", "import", "info", "lint", "link",
	"list", "note", "orphans", "priority", "promote", "prune", "purge", "q", "query",
	"ready", "recompute-blocked", "reclaim", "reopen", "search", "set-state", "show",
	"stale", "status", "statuses", "tag", "types", "unclaim", "undefer", "update",

	// comments, dependencies, labels, key-value
	"comments", "comments add", "comments list",
	"dep", "dep add", "dep cycles", "dep list", "dep relate", "dep remove", "dep tree", "dep unrelate",
	"kv clear", "kv get", "kv list", "kv set",
	"label add", "label list", "label list-all", "label propagate", "label remove",

	// molecules, epics, todos, state
	"epic close-eligible", "epic status",
	"mol bond", "mol burn", "mol current", "mol distill", "mol last-activity", "mol pour",
	"mol progress", "mol ready", "mol seed", "mol show", "mol squash", "mol stale",
	"mol wisp", "mol wisp create", "mol wisp gc", "mol wisp list",
	"state", "state list", "todo", "todo add", "todo done", "todo list",

	// gates, rules, provenance, audit, upgrade review
	"audit label", "audit record",
	"gate add-waiter", "gate check", "gate create", "gate list", "gate resolve", "gate show",
	"provenance by-ref", "provenance log", "provenance record",
	"rules audit", "rules compact",
	"upgrade ack", "upgrade review", "upgrade status",

	// workspace lifecycle and configuration
	"bootstrap", "config apply", "config drift", "config get", "config list", "config set",
	"config set-many", "config show", "config unset", "config validate", "context",
	"init", "init-safety", "onboard", "preflight", "quickstart", "schema", "setup", "where",
	"version", "worktree create", "worktree info", "worktree list", "worktree remove",

	// storage, history and migration paths that are already routed or store-free
	"compact", "events export", "events prune", "events tail",
	"dolt clean-databases", "dolt killall", "dolt set", "dolt show", "dolt status",
	"dolt start", "dolt stop", "dolt test",
	"migrate from-proxied-server-to-server", "migrate from-proxied-server-to-shared-server",
	"migrate from-server-to-proxied-server", "migrate from-shared-server-to-proxied-server",
	"migrate legacy-sqlite", "migrate schema",

	// formulas (parser-only), agent memory, swarm reads
	"formula convert", "formula list", "formula schema", "formula show",
	"mail", "memories", "ping", "prime", "recall", "remember",
	"swarm status", "swarm validate",

	// human-in-the-loop queue
	"human", "human dismiss", "human list", "human respond", "human stats",

	// trackers: the verbs with no proxied refusal of any kind
	"ado pull", "ado push", "github pull", "github push", "gitlab pull", "gitlab push",
	"jira pull", "jira push", "jira status", "jira sync",
	"linear pull", "linear push", "linear status", "linear sync", "linear teams",
	"notion pull", "notion push",

	// hooks, metrics, serving and internal entry points
	"hooks install", "hooks list", "hooks run", "hooks uninstall",
	"metrics", "metrics example", "metrics off", "metrics on",
	"serve", "sql",
	"codex-hook", "cursor-hook", "db-proxy-child", "send-metrics",
}

func init() {
	for _, path := range proxyPermittedPaths {
		proxyCapabilityRegistry = append(proxyCapabilityRegistry, permitted(path))
	}
	proxyCapabilityIndex = make(map[string]capabilityRow, len(proxyCapabilityRegistry))
	for _, row := range proxyCapabilityRegistry {
		proxyCapabilityIndex[capabilityKey(row.Path, row.ArgSet)] = row
	}
}

// proxyCapabilityIndex is proxyCapabilityRegistry keyed for lookup. Duplicate
// keys are rejected by TestProxyCapabilityRegistryHasNoDuplicateRows rather
// than by a panic here: a typo must fail the build, not every bd invocation.
var proxyCapabilityIndex map[string]capabilityRow

func capabilityKey(path, argSet string) string { return path + "\x00" + argSet }

// LookupCapabilityRow returns the row for an exact path/arg-set key.
func LookupCapabilityRow(path, argSet string) (capabilityRow, bool) {
	row, ok := proxyCapabilityIndex[capabilityKey(path, argSet)]
	return row, ok
}

// capabilityRowFor resolves a command to its row, preferring the row keyed on
// the flags the invocation actually set over the bare path.
//
// The fallback to the bare row is what lets one table serve both halves of the
// old policy: a path row refuses whatever flags were passed unless a more
// specific row says otherwise, so `backup sync --dry-run` is still refused by
// the `backup sync` row. The predecessor of the arg-set half matched exactly or
// not at all, which meant an unlisted flag combination on a refused path
// slipped through; no such combination exists among the commands that carry
// these flags, so nothing changes today, and the fallback is why none can
// appear later.
func capabilityRowFor(cmd *cobra.Command) (capabilityRow, bool) {
	path := commandRegistryPath(cmd)
	if argSet := commandPolicyArgSet(cmd); argSet != "" {
		if row, ok := LookupCapabilityRow(path, argSet); ok {
			return row, true
		}
	}
	return LookupCapabilityRow(path, "")
}

// commandRegistryPath is the registry key for a command: its full path with the
// root name removed, for example "dolt remote add".
func commandRegistryPath(cmd *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
}

// proxyPolicyArgFlags are the flags that take part in a capability key. Only
// flags that alter a command's execution belong here: output, tracing and other
// inherited root flags must never bypass a refusal by changing the key.
var proxyPolicyArgFlags = map[string]bool{
	"auto-merge": true, "dry-run": true, "repair": true, "of": true, "with": true,
}

// commandPolicyArgSet renders the set flags that take part in the key, sorted so
// the key does not depend on the order the user typed them.
func commandPolicyArgSet(cmd *cobra.Command) string {
	var args []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if proxyPolicyArgFlags[f.Name] {
			args = append(args, "--"+f.Name)
		}
	})
	sort.Strings(args)
	return strings.Join(args, " ")
}

// validateProxyRegistryBeforeProvider is the path-keyed half of the proxied
// front door: it rejects direct-only commands before migrations, auto-start or
// provider construction, so a refusal leaves the workspace untouched. The
// flag-keyed half is validateProxyCapabilitiesBeforeProvider.
//
// topology is the workspace's proxied shape (resolveProxiedTopology). It is a
// parameter rather than a lookup inside so the gate can be exercised for every
// shape without a workspace on disk.
func validateProxyRegistryBeforeProvider(cmd *cobra.Command, topology ProxyTopology) error {
	if cmd == nil {
		return nil
	}
	// `compact` is the one rule that a flag VALUE decides rather than a flag's
	// presence, so it cannot be expressed as an arg-set row. `bd compact` and
	// `bd rules compact` have no --dolt flag at all and are never refused here;
	// `bd admin compact --dolt` has a proxied route and falls through to it.
	if cmd.Name() == "compact" {
		if cmd.Flags().Lookup("dolt") == nil {
			return nil
		}
		if dolt, _ := cmd.Flags().GetBool("dolt"); dolt {
			return nil
		}
	}
	row, ok := capabilityRowFor(cmd)
	if !ok {
		return nil
	}
	rule := row.ruleFor(topology)
	if rule.Outcome != ProxyOutcomeRefused {
		return nil
	}
	return HandleProxyCapabilityError(proxyCapabilityErrorFor(rule))
}

func proxyCapabilityErrorFor(rule proxyCapabilityRule) *ProxyCapabilityError {
	return &ProxyCapabilityError{
		Code:     rule.Code,
		Message:  rule.Message,
		ExitCode: rule.ExitCode,
		Mutates:  rule.Mutates,
		Reason:   rule.Reason,
	}
}

// HistoryCapabilityClass classifies history/version-control commands on the
// proxied topology without coupling them to a particular backend engine.
type HistoryCapabilityClass string

const (
	HistoryProxySupported HistoryCapabilityClass = "proxy-supported"
	HistoryDirectOnly     HistoryCapabilityClass = "direct-only"
)

// LookupHistoryCapability classifies an exact command path. Paths with no
// history classification are intentionally absent so callers cannot
// accidentally advertise support.
func LookupHistoryCapability(commandPath string) (HistoryCapabilityClass, bool) {
	row, ok := LookupCapabilityRow(commandPath, "")
	if !ok || row.History == "" {
		return "", false
	}
	return row.History, true
}
