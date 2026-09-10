package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/net/netutil"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// APIVersion is the path major this package serves, reported as
// ContextResponse.api_version. It changes only when /v1 is cut.
const APIVersion = "v0"

// The operating envelope. Every one of these is a bound on how much of the
// process a client can occupy, and the two that matter most are the ones with
// no natural limit: without semAcquireTimeout a queue behind a wedged database
// grows without end, and without requestDeadline a request that got a slot
// never gives it back. Both are deliberately generous — this is a loopback
// service for automation clients, not a public endpoint — and all of them can
// become operator flags later without touching the wire.
const (
	// maxInflight bounds handlers that touch the database. Every unit of work
	// pins one SQL connection, so this is also the steady-state connection
	// count.
	maxInflight = 16
	// maxConns bounds ACCEPTED connections. The semaphore does not: Go spawns
	// a goroutine per connection, and one parked on a full semaphore still
	// holds its goroutine, fd and buffers. Excess connections wait in the
	// kernel accept backlog instead of in Go memory.
	maxConns = 64
	// semAcquireTimeout bounds the queue in TIME as well as width. A timed-out
	// acquisition is the already-documented 503 busy, so shedding load
	// introduces no new status vocabulary.
	semAcquireTimeout = 10 * time.Second
	// requestDeadline is the whole-request backstop, needed because
	// WriteTimeout is 0 (below). It covers semaphore wait + unit of work +
	// query, and deliberately not the response write.
	requestDeadline = 60 * time.Second
	// saturationWarn is how long a semaphore wait has to last before it is
	// worth a log line of its own. This is the wedge-detection signal: /healthz
	// stays green while the database is hung, so saturation events are what
	// distinguish "wedged" from "no traffic".
	saturationWarn = time.Second
	// drainTimeout covers a claim inside its serialization-retry budget plus
	// the commit, so a graceful shutdown does not kill a connection whose write
	// may already have landed.
	drainTimeout = 20 * time.Second
	// uowCloseTimeout bounds the DETACHED close described on WithUOW.
	uowCloseTimeout = 5 * time.Second
)

// Pool limits for the provider's *sql.DB. The semaphore bounds handlers, not
// connections: a poisoned connection replaced after a failed ROLLBACK, each
// retry attempt of a committing transaction (a fresh unit of work is a fresh
// pinned connection), and any semaphore-exempt handler that later touches the
// database all escape it.
var servePoolLimits = uow.PoolLimits{
	MaxOpenConns:    maxInflight + 4,
	MaxIdleConns:    maxInflight,
	ConnMaxIdleTime: 5 * time.Minute,
	ConnMaxLifetime: time.Hour,
}

// HTTP-level timeouts. WriteTimeout is deliberately absent: `limit=0` means
// unlimited on both list operations, and a whole-response deadline would
// truncate a large body mid-write.
//
// writeStallTimeout is what replaces it — a deadline rolled forward before
// every write (statusWriter.extendWriteDeadline), which bounds a STALLED write
// without bounding total transfer. That bound is load-bearing, not hygiene:
// route() releases the database slot when the handler returns, and the handler
// returns only after writing the body, so without it a client that opens
// maxInflight requests and then stops reading pins every slot and its pinned
// connection until the process is restarted — while /healthz stays green. A
// context deadline cannot substitute: nothing cancels a blocked socket write.
//
// The read, header and idle timeouts bound request READING and keep-alive idle.
// They say nothing about a response write, and must not be cited as if they did.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
	writeStallTimeout = 30 * time.Second
	maxHeaderBytes    = 64 << 10
)

// Config is everything the server needs to answer. It is assembled by the
// caller — the package resolves no workspace state of its own.
type Config struct {
	// Addr is the host:port to bind. The host must be a numeric IP literal;
	// see ValidateBindAddr.
	Addr string
	// AllowNonLoopback permits a bind beyond loopback. There is no TLS, so
	// this is an operator decision that is never taken by default, and it
	// requires either Auth or InsecureNoAuth — see ValidateAuthPosture.
	AllowNonLoopback bool
	// Auth verifies bearer credentials. NIL MEANS NO AUTHENTICATION, which is
	// the pre-existing behavior and stays the default on loopback: a zero
	// Config serves exactly what it served before this field existed.
	Auth *TokenFileAuth
	// InsecureNoAuth is the operator's explicit waiver for serving a
	// non-loopback bind with no credential. It only ever permits; it never
	// disables a configured Auth (that combination is refused).
	InsecureNoAuth bool
	// AllowedHosts are extra Host header values to answer to, beyond the
	// loopback spellings and the bind address. In a cluster the client dials a
	// service DNS name, which the rebinding defense would otherwise refuse;
	// see newHostPolicy. Empty leaves today's policy exactly as it was.
	AllowedHosts []string
	// Provider is where every database-touching handler opens its one unit of
	// work per request.
	Provider uow.UnitOfWorkProvider
	// The fields below are the roles this server answers from, for a backend
	// whose facade is a STORE rather than a unit-of-work provider. sourceRoles
	// is the authoritative list.
	//
	// Set them ALL, and only when Provider is nil: together they are the other
	// complete database source, not an override of one. Listen refuses every
	// other combination, including a partial set — a server missing one role
	// would bind, answer every other route, and fail that one with a nil
	// dereference inside a handler on a live server, which is worse than not
	// starting.
	//
	// A caller with a store takes them off the store's own accessors, and WHICH
	// store value it takes them off is the whole question. Every decorator a
	// store wears is on the value its accessor returns — that is what the
	// accessors are for — and bd's chain is
	// HookFiringStore -> InstrumentedStorage -> raw, so `store.IssueClaimer()`
	// there returns a claimer that runs the workspace's on_update hook script
	// for every claim it lands. That is precisely what this server documents it
	// does not do (cmd/bd/serve.go). Take the roles from BENEATH the hook layer:
	//
	//	src := store
	//	if hooked, ok := src.(*storage.HookFiringStore); ok {
	//		src = hooked.Unwrap() // keeps the telemetry layer, drops the hooks
	//	}
	//	rd, err := src.IssueReader()
	//	cl, err := src.IssueClaimer()
	//	... one per field below, all off the same src ...
	//	httpapi.Listen(httpapi.Config{Reader: rd, Claimer: cl, /* and the rest */})
	//
	// cmd/bd's serveIssueRoles is that loop, written out.
	//
	// Listen refuses a hook-firing role rather than trusting the paragraph
	// above — see checkDatabaseSource.
	//
	// WHAT LISTEN CANNOT CHECK, and the caller therefore owns: each call must
	// commit ON ITS OWN, atomically and durably. That is what this server's
	// contract states per request, and nothing here can observe the commit
	// protocol of the backend behind an interface — every check available would
	// be a self-declaration by the same caller-supplied code being checked.
	// Embedded Dolt is the backend that does not qualify (its commit runs
	// outside the SQL transaction on a separate connection) and it is refused
	// where the workspace is actually known: serveDatabaseSource in
	// cmd/bd/serve.go.
	//
	// Unlike the provider path these are built ONCE, before Listen, rather than
	// per request. The provider path rebuilds its roles per request for exactly
	// one reason — so the units of work they open land in that request's uow_ms
	// (see Server.reader) — and a role reached this way opens none through this
	// server, so a rebuild would buy nothing.
	Reader  issueops.Reader
	Claimer issueops.Claimer
	// BatchCloser closes many issues as one transaction, behind
	// POST /v0/beads/issues:batchClose. It is its own field rather than a mode
	// of Lifecycle for the role's reason: the request is the transaction
	// boundary, and a loop over Lifecycle.Close is N transactions.
	BatchCloser issueops.BatchCloser
	// ReadyClaimer is the atomic take of ready work, behind
	// POST /v0/beads/issues:claimNext. It is its own field rather than a second
	// verb on Claimer for the reason the role is its own interface: the caller
	// names a QUESTION and the implementation picks the answer, so selection is
	// part of the operation and not a patch.
	ReadyClaimer issueops.ReadyClaimer
	// Releaser is the claim's inverse, behind
	// POST /v0/beads/issues/{id}:release. It is its own field rather than a
	// method on Claimer for the reason the role is its own interface: a caller
	// entitled to give its own work back is very often not entitled to take
	// new work, so a surface carrying both hands out a capability it should not
	// be able to reach.
	Releaser issueops.Releaser
	// Lifecycle is the guarded-mutation role behind the issue lifecycle
	// operations. Required on the same terms as every field here, and the
	// hook-firing refusal below bites hardest on it: a store's own
	// IssueLifecycle() returns a role that fires on_create, on_update and the
	// close hooks for every mutation it lands.
	Lifecycle     issueops.Lifecycle
	Settings      issueops.WorkspaceConfig
	Stats         issueops.StatsReporter
	CycleDetector issueops.CycleDetector
	EdgeReader    issueops.EdgeReader
	// GraphCounter is the edge-count role behind
	// GET /v0/beads/dependencies:count. It is a SEPARATE field from EdgeReader
	// for the reason Counter is separate from ReadyCounter: that role answers
	// with the edge ROWS in one direction, this one with a number in either,
	// and neither can answer the other's question. Required on the same terms
	// as every field here.
	GraphCounter issueops.GraphCounter
	// Relations is the single-anchor neighbor read behind
	// GET /v0/beads/issues/{id}/related. It is a SEPARATE field from EdgeReader
	// for the reason issueops.EdgeReader's own doc gives at length: that role
	// answers with the edge ROWS for many anchors and reports a miss per anchor,
	// this one answers with the hydrated ISSUES on the far end for ONE anchor and
	// answers ErrNotFound. Different answer shape, different miss policy,
	// different arity. Required on the same terms as every field here.
	Relations issueops.Relations
	// Commenter is the append-one-comment role behind
	// POST /v0/beads/issues/{id}/comments. It is its own field rather than a
	// verb on Lifecycle for the role's own reason: a comment is not a patch to
	// an issue — it appends a row to a thread the issue owns and leaves every
	// field of the issue untouched, so an IssuePatch has nothing to carry and an
	// UpdateResult has nowhere to put the comment. Required on the same terms as
	// every field here.
	Commenter         issueops.Commenter
	BlockingAnnotator issueops.BlockingAnnotator
	TreeWalker        issueops.TreeWalker
	ReadyCounter      issueops.ReadyCounter
	// Counter is the issue-count role behind GET /v0/beads/issues:count. It is
	// a SEPARATE field from ReadyCounter because it is a separate role: that
	// one sizes the ready predicate, this one sizes a filter, and neither can
	// answer the other's question. Required on the same terms as every field
	// here.
	Counter issueops.Counter
	Querier issueops.Querier
	// Sweeper is the DESTRUCTIVE one, required on the same terms as every other
	// role rather than opt-in: whether this build erases beads is a decision
	// for the operator who chose to run bd serve, not a consequence of whether
	// a caller remembered a field.
	Sweeper issueops.Sweeper
	// Deleter is the OTHER destructive one, required for the same reason.
	Deleter      issueops.Deleter
	BatchCreator issueops.BatchCreator
	// DependencyEditor is the graph's write side. Required on the same terms as
	// the two destructive roles above: whether this build can rewire the
	// dependency graph is a decision for the operator who chose to run bd serve,
	// not a consequence of whether a caller remembered a field.
	DependencyEditor issueops.DependencyEditor
	// MetadataCAS is the conditional single-key metadata write behind
	// POST /v0/beads/issues/{id}:casMetadata. Required on the same terms as
	// every other role: a Config missing it would bind and nil-dereference on
	// the first request that reached that handler.
	MetadataCAS issueops.MetadataCAS
	// BatchApplier is the ordered, heterogeneous plan behind
	// POST /v0/beads/issues:batchApply. Required on the same terms as every
	// other role: a Config missing it would bind and nil-dereference on the
	// first request that reached that handler.
	//
	// It is the field where the hook-firing refusal below bites HARDEST. A
	// store's own accessor returns an applier that fires on_create, on_update
	// AND the close hooks — once per landed item, plus once per distinct edge
	// source — so a single hundred-item request served unpeeled would run a
	// hundred of the workspace's subprocesses inside one HTTP call.
	BatchApplier issueops.BatchApplier
	// Memories is the workspace's persistent memory plane, and the one field
	// here that is not an issueops role: memories are user data riding in the
	// config table under their own merge class, not settings, so they have
	// their own leaf package. Required on the same terms as every field above —
	// a partial set is refused, so the field and the operations that reach it
	// land together.
	Memories memoryops.Memories
	// EventsJournal is the durable mutation journal's READ side, and the ONE
	// role here that is required CONDITIONALLY — which is why it is absent from
	// sourceRoles and checked on its own. Like Memories it is not an issueops
	// role: the journal is a replay feed over engine state on a dolt_ignored
	// table, not a bead query, so it has its own leaf package (journalops,
	// whose doc states why). storage.EventsJournalCursor is an ALIAS of
	// journalops.Journal, so this field names the role whichever spelling
	// reaches it.
	//
	// Required exactly when EventsJournalEnabled. A workspace that records
	// nothing needs no reader, and a storage backend that cannot read the
	// journal at all is a perfectly ordinary backend as long as nobody asked it
	// to journal — the same deal eventsjournal.Apply takes when it binds
	// activation to a store. Demanding it unconditionally would make a
	// capability nothing uses a precondition for running the server.
	//
	// It is the READ role and deliberately NOT the wider
	// storage.EventsJournalAccessor the CLI takes. That interface also prunes,
	// and a server that is documented to publish the journal and never retain
	// it should not be holding a delete it merely promises not to call.
	EventsJournal storage.EventsJournalCursor
	// EventsJournalEnabled is whether the served workspace actually records
	// mutations, resolved by the caller through eventsjournal.EnabledFor.
	//
	// It is a resolved BOOLEAN rather than something this package works out,
	// for the reason Config gives at the top: activation reads the target
	// workspace's own config.yaml and the BD_EVENTS_JOURNAL environment
	// override, which is workspace state, and this package resolves none.
	//
	// It cannot be inferred from the data either, which is the whole reason the
	// field exists. A disabled journal presents as zero rows and a head of
	// zero — byte-identical to an enabled journal nothing has written yet — so
	// a server without this flag would answer "you are caught up" to a consumer
	// polling a workspace that will never emit a record.
	EventsJournalEnabled bool
	// Workspace is the startup snapshot GET /v0/beads/context answers from.
	// Only the allowlisted fields are ever serialized — see contextResponse,
	// which names the whole set and the reasons for the exclusions.
	Workspace domain.ContextInfo
	// SchemaVersion is the CLI's stdout JSON envelope version, reported for
	// diagnostics. Clients are told not to branch on it.
	SchemaVersion int
	// Mode names the resolved storage topology ("proxied", "external") for the
	// startup log line. Cosmetic: nothing dispatches on it.
	Mode string
	// Stdout receives exactly one line, the bound address, so a caller that
	// asked for an ephemeral port can discover it. Stderr receives the
	// operational log. Both default to the process streams.
	Stdout io.Writer
	Stderr io.Writer
}

// Server is one bound listener and the routes behind it. Build it with Listen,
// which binds before returning so the caller can read Addr, then run Serve.
type Server struct {
	cfg      Config
	provider uow.UnitOfWorkProvider

	// The configured roles, set exactly when provider is nil. They are what
	// reader(), claimer(), cycleDetector() and the rest of those accessors hand
	// back on the store-shaped source; the field names differ from the method
	// names because a struct cannot carry both.
	issueReader       issueops.Reader
	issueClaimer      issueops.Claimer
	issueBatchCloser  issueops.BatchCloser
	issueReadyClaimer issueops.ReadyClaimer
	issueReleaser     issueops.Releaser
	issueLifecycle    issueops.Lifecycle
	settings          issueops.WorkspaceConfig
	issueStats        issueops.StatsReporter
	issueCycles       issueops.CycleDetector
	issueEdges        issueops.EdgeReader
	issueEdgeCounter  issueops.GraphCounter
	issueRelations    issueops.Relations
	issueCommenter    issueops.Commenter
	issueBlocking     issueops.BlockingAnnotator
	issueTree         issueops.TreeWalker
	issueReadyCounter issueops.ReadyCounter
	issueCounter      issueops.Counter
	issueQuerier      issueops.Querier
	issueSweeper      issueops.Sweeper
	issueDeleter      issueops.Deleter
	issueBatchCreator issueops.BatchCreator
	issueDependencies issueops.DependencyEditor
	issueMetadataCAS  issueops.MetadataCAS
	issueBatchApplier issueops.BatchApplier
	workspaceMemories memoryops.Memories
	eventsJournal     storage.EventsJournalCursor

	listener net.Listener
	http     *http.Server

	// sem bounds handlers that touch the database. Buffered channel rather
	// than sync.Semaphore so the acquisition can select on a timer.
	sem chan struct{}
	// auth is nil on an unauthenticated server, which is the loopback default.
	auth *TokenFileAuth
	// semTimeout, semWarn, writeStall, watchPoll and watchBeat default to the
	// constants above. They are fields rather than constants at the point of use
	// so the queueing, stalled-write and streaming behavior can be exercised in
	// milliseconds instead of tens of seconds.
	semTimeout time.Duration
	semWarn    time.Duration
	writeStall time.Duration
	watchPoll  time.Duration
	watchBeat  time.Duration

	// closing is closed when a graceful shutdown begins, and it is the ONLY
	// notice a streaming handler gets: http.Server.Shutdown waits for active
	// requests without canceling their contexts, so a stream that watched only
	// for a client disconnect would hold every shutdown open for the whole drain
	// timeout and then be killed anyway. Registered on the http.Server (which
	// calls it exactly once) and guarded so a second call cannot close twice.
	closing     chan struct{}
	closingOnce sync.Once

	// watchStreams is the live count of open events:watch streams and
	// maxWatchStreams mirrors the constant, so a test can saturate the cap
	// without opening sixty-four of them.
	watchStreams    atomic.Int64
	maxWatchStreams int

	log     *log.Logger
	stdout  io.Writer
	ctxBody apigen.ContextResponse

	// hosts is the Host-header allowlist, the DNS-rebinding defense. It is
	// derived from the bind address and there is no configuration that turns it
	// off; see newHostPolicy.
	hosts hostPolicy

	idPrefix string
	idSeq    atomic.Uint64

	// maxConns mirrors the constant so a test can exercise the cap without
	// opening 64 sockets. liveConns is the accepted-connection gauge, reported
	// on every request line; connCapWarned makes the saturation event
	// edge-triggered rather than once per connection.
	maxConns      int
	liveConns     atomic.Int64
	connCapWarned atomic.Bool
}

// ValidateBindAddr enforces the bind posture, following the policy the managed
// Dolt child already lives under (validateManagedServerConfigPolicy in
// cmd/bd/proxied_server.go): the host must be a NUMERIC IP literal.
//
// Hostnames are refused, "localhost" included. A name is not a listener
// specification — it resolves to whatever the host's resolver says today, so
// the operator cannot tell from the flag which interfaces they just opened.
// Unix sockets are not supported at all; they fail here because they do not
// parse as host:port.
func ValidateBindAddr(addr string, allowNonLoopback bool) (net.IP, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("--addr %q must be HOST:PORT with a numeric IP literal host (unix sockets are not supported): %w", addr, err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, fmt.Errorf("--addr %q: port must be a number from 0 to 65535 (0 picks an ephemeral port)", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("--addr %q: host must be a numeric IP literal, not a name — use 127.0.0.1 rather than localhost", addr)
	}
	if !ip.IsLoopback() && !allowNonLoopback {
		return nil, fmt.Errorf("--addr %q binds beyond loopback, which requires --allow-non-loopback (and, with it, --auth-token-file)", addr)
	}
	return ip, nil
}

// Listen validates the configuration, binds the listener, and reports the
// bound address on stdout and the startup state on stderr. It does not accept
// anything until Serve runs.
//
// There is no lock file, pid file or discovery file: bd serve is
// operator-invoked and the TCP bind IS the mutual exclusion, so a second
// instance on the same fixed port fails here with the operating system's own
// address-in-use error. (Under the ephemeral default that exclusion does not
// exist — N instances simply run on N ports — which is why fixed ports are the
// deployment recommendation.)
func Listen(cfg Config) (*Server, error) {
	if err := checkDatabaseSource(cfg); err != nil {
		return nil, err
	}
	ip, err := ValidateBindAddr(cfg.Addr, cfg.AllowNonLoopback)
	if err != nil {
		return nil, err
	}
	// The same posture and allowlist rules the CLI applies, applied again here
	// so a second caller of this package cannot assemble a Config that serves
	// the whole surface to a network with no credential.
	if err := ValidateAuthPosture(cfg.AllowNonLoopback, cfg.Auth != nil, cfg.InsecureNoAuth); err != nil {
		return nil, err
	}
	for _, host := range cfg.AllowedHosts {
		if err := ValidateAllowedHost(host); err != nil {
			return nil, err
		}
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	prefix, err := newIDPrefix()
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:               cfg,
		provider:          cfg.Provider,
		issueReader:       cfg.Reader,
		issueClaimer:      cfg.Claimer,
		issueBatchCloser:  cfg.BatchCloser,
		issueReadyClaimer: cfg.ReadyClaimer,
		issueReleaser:     cfg.Releaser,
		issueLifecycle:    cfg.Lifecycle,
		settings:          cfg.Settings,
		issueStats:        cfg.Stats,
		issueCycles:       cfg.CycleDetector,
		issueEdges:        cfg.EdgeReader,
		issueEdgeCounter:  cfg.GraphCounter,
		issueRelations:    cfg.Relations,
		issueCommenter:    cfg.Commenter,
		issueBlocking:     cfg.BlockingAnnotator,
		issueTree:         cfg.TreeWalker,
		issueReadyCounter: cfg.ReadyCounter,
		issueCounter:      cfg.Counter,
		issueQuerier:      cfg.Querier,
		issueSweeper:      cfg.Sweeper,
		issueDeleter:      cfg.Deleter,
		issueBatchCreator: cfg.BatchCreator,
		issueDependencies: cfg.DependencyEditor,
		issueMetadataCAS:  cfg.MetadataCAS,
		issueBatchApplier: cfg.BatchApplier,
		workspaceMemories: cfg.Memories,
		eventsJournal:     cfg.EventsJournal,

		sem:        make(chan struct{}, maxInflight),
		semTimeout: semAcquireTimeout,
		semWarn:    saturationWarn,

		closing:         make(chan struct{}),
		maxWatchStreams: maxWatchStreams,

		log:      log.New(cfg.Stderr, "bd serve: ", log.LstdFlags|log.LUTC),
		stdout:   cfg.Stdout,
		ctxBody:  contextResponse(cfg.Workspace, cfg.SchemaVersion, Capabilities()),
		hosts:    newHostPolicy(ip, cfg.AllowedHosts),
		auth:     cfg.Auth,
		idPrefix: prefix,
		maxConns: maxConns,
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", cfg.Addr, err)
	}
	s.listener = netutil.LimitListener(ln, s.maxConns)

	s.http = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(cfg.Stderr, "bd serve: http: ", log.LstdFlags|log.LUTC),
		ConnState:         s.connState,
	}
	// Tell the streams to wind up as soon as a drain starts. Without it a
	// graceful shutdown waits out the whole drain timeout on any open stream and
	// then reports itself forced, which is the one shutdown signal an operator
	// is meant to be able to trust.
	s.http.RegisterOnShutdown(s.closeStreams)

	// Bound what a burst of requests can open on the database. The knob is
	// optional on the interface, so say so out loud when a provider does not
	// carry it rather than silently running unbounded.
	//
	// Nothing to bound on the roles source: the pool belongs to whatever the
	// backend is, and this server neither owns it nor can reach it. Saying the
	// knob is "unavailable" there would report a missing capability for a
	// provider that was never asked for.
	if cfg.Provider != nil {
		if tuner, ok := cfg.Provider.(uow.PoolTuner); ok {
			tuner.SetPoolLimits(servePoolLimits)
		} else {
			s.event("pool_limits_unavailable", "provider", fmt.Sprintf("%T", cfg.Provider))
		}
	}

	fmt.Fprintf(s.stdout, "bd serve: listening on http://%s\n", s.Addr())
	s.logStartup()
	return s, nil
}

// checkDatabaseSource enforces exactly one complete database source.
//
// There are two, and a Config carries one or the other: a unit-of-work
// provider, or the roles this surface answers from (sourceRoles). A PARTIAL
// set is refused with the same message as none at all, because it is the same
// mistake and the failure it would otherwise produce is the worst shape
// available — a Config missing one role binds, answers every other route, and
// fails that one with a nil dereference in a handler on a live server.
//
// The set GROWS as this surface grows: every operation added here is an
// operation a roles-backed deployment must be able to answer, so a role added
// to the set turns "this build serves an operation your Config cannot answer"
// into a startup error instead of a 500 on the first client that finds it.
//
// Both together is refused rather than resolved by precedence: a caller that
// set both holds two different opinions about where this server reads from, and
// silently honoring one of them leaves the other as configuration that looks
// live and is not.
//
// The last refusal is the one a caller does not see coming. A store wears
// decorators and its accessors hand them out — that is what the accessors are
// FOR — so the obvious `store.IssueClaimer()` on bd's own storage chain returns
// a claimer that fires the workspace's on_update hook for every claim it lands.
// This server's contract says hooks do not fire (cmd/bd/serve.go), and a
// contract broken by the caller's most natural line is not a contract. Refusing
// at Listen is the difference between a startup error naming the store to take
// roles from and a server that has been quietly running a user's subprocess per
// claim since it booted.

// sourceRoles is the store-shaped source's roles in ONE place, so the three
// questions checkDatabaseSource asks — is any set, is any missing, does any
// fire hooks — cannot drift apart as the set grows. An operation that reaches a
// role this source does not yet carry adds a line here and a line to
// roleSourceNames, and nothing else in this file.
//
// A role is compared against nil as an INTERFACE, which is what the caller
// actually sets; a typed nil stored in one of these fields is a value as far as
// this check is concerned.
//
// It carries only the roles every deployment must have. EventsJournal is
// deliberately NOT here: it is required only when the workspace's journal is
// enabled, and folding a conditional field into a set whose whole value is
// "all or nothing" would turn an honest condition into a special case inside
// three functions. It is checked once, on its own, below.
func sourceRoles(cfg Config) []any {
	return []any{cfg.Reader, cfg.Claimer, cfg.ReadyClaimer, cfg.Releaser, cfg.Lifecycle, cfg.BatchCloser, cfg.Settings, cfg.Stats, cfg.CycleDetector, cfg.EdgeReader, cfg.GraphCounter, cfg.Relations, cfg.Commenter, cfg.BlockingAnnotator, cfg.TreeWalker, cfg.ReadyCounter, cfg.Counter, cfg.Querier, cfg.Sweeper, cfg.Deleter, cfg.BatchCreator, cfg.DependencyEditor, cfg.BatchApplier, cfg.Memories, cfg.MetadataCAS}
}

// roleSourceNames spells sourceRoles for the refusal message, in the same
// order, so a caller reading the error learns the whole set it must pass.
const roleSourceNames = "Reader, Claimer, ReadyClaimer, Releaser, Lifecycle, BatchCloser, Settings, Stats, CycleDetector, EdgeReader, GraphCounter, Relations, Commenter, BlockingAnnotator, TreeWalker, ReadyCounter, Counter, Querier, Sweeper, Deleter, BatchCreator, DependencyEditor, BatchApplier, Memories and MetadataCAS"

func anyRoleSet(cfg Config) bool {
	return slices.ContainsFunc(sourceRoles(cfg), func(r any) bool { return r != nil })
}

func everyRoleSet(cfg Config) bool {
	return !slices.Contains(sourceRoles(cfg), nil)
}

func anyRoleFiresHooks(cfg Config) bool {
	return slices.ContainsFunc(sourceRoles(cfg), storage.RoleFiresHooks)
}

func checkDatabaseSource(cfg Config) error {
	switch {
	case cfg.Provider != nil && (anyRoleSet(cfg) || cfg.EventsJournal != nil):
		return errors.New("httpapi: both a unit-of-work provider and issue roles were set; pass exactly one database source")
	case cfg.Provider == nil && !everyRoleSet(cfg):
		return errors.New("httpapi: no database source: set Provider, or " + roleSourceNames + " together")
	// The conditional role, checked where every other configuration mistake is.
	// A workspace that HAS a journal and a server that cannot read it is the
	// one combination that would bind, answer every other route, and fail this
	// one — with a nil dereference, which is the shape checkDatabaseSource
	// exists to prevent. A workspace with the journal off needs no reader and
	// this says nothing about it.
	case cfg.Provider == nil && cfg.EventsJournalEnabled && cfg.EventsJournal == nil:
		return errors.New("httpapi: this workspace's events journal is enabled but no EventsJournal reader was configured; " +
			"take one off the store (storage.EventsJournalCursor), or serve a workspace with the journal off")
	case anyRoleFiresHooks(cfg):
		return errors.New("httpapi: a configured role fires this workspace's hooks; " +
			"this server does not run hooks, so take the roles from the store beneath the hook decorator " +
			"((*storage.HookFiringStore).Unwrap)")
	case uow.ProviderFiresHooks(cfg.Provider):
		// The same refusal for the other database source. A provider's roles
		// carry whatever the provider carries, so a hook-firing one would run a
		// user's subprocess per served mutation just as a hook-firing role does.
		return errors.New("httpapi: the configured provider fires this workspace's hooks; " +
			"this server does not run hooks, so pass the provider beneath the hook layer " +
			"(uow.UnwrapProvider)")
	}
	return nil
}

// Addr is the bound address, which is the only way to discover the port under
// the ephemeral default.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Serve accepts requests until ctx is canceled, then drains. It returns nil
// on a clean shutdown; a listener failure is returned as-is.
//
// The drain budget covers a committing request that is mid-retry, because
// Shutdown does not cancel in-flight handler contexts: killing such a
// connection early would leave the client unable to tell whether its write
// landed.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(s.listener) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.event("shutdown_start", "drain_timeout", drainTimeout.String(), "conns", s.liveConns.Load())

	// Detached: ctx is already canceled, and the drain is the point.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
	defer cancel()

	if err := s.http.Shutdown(drainCtx); err != nil {
		killed := s.liveConns.Load()
		_ = s.http.Close()
		s.event("shutdown_forced", "conns_killed", killed, "reason", err.Error())
	} else {
		s.event("shutdown_complete")
	}
	<-errCh
	return nil
}

// connState tracks accepted connections, and says out loud when the cap is
// reached.
//
// It has to: netutil.LimitListener simply stops calling Accept at the cap, so
// further connections wait in the kernel backlog with nothing on stderr — and
// /healthz needs a fresh accept too, so an exhausted cap is indistinguishable
// from no traffic at all. Request lines just stop. This is the connection
// tier's version of the semaphore's saturation event, and the one wedge mode
// this slice can actually exhibit.
//
// The event is edge-triggered: once when the cap is reached, again only after
// it has cleared. The conns gauge on every request line is what shows it
// climbing beforehand.
func (s *Server) connState(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		n := s.liveConns.Add(1)
		if s.maxConns > 0 && n >= int64(s.maxConns) && s.connCapWarned.CompareAndSwap(false, true) {
			s.event("conn_cap_saturated", "conns", n, "max_conns", s.maxConns)
		}
	case http.StateHijacked, http.StateClosed:
		if s.liveConns.Add(-1) < int64(s.maxConns) {
			s.connCapWarned.Store(false)
		}
	}
}

// reader returns the issue-query surface for one request.
//
// On the ROLES source it is the configured role. There is nothing to build: a
// store's accessor already answered for its whole decorator chain when the
// caller called it, and this server opens no units of work on that path.
//
// On the PROVIDER source it is built per request rather than once at startup so
// that the units of work it opens are timed into THIS request's log line. That
// is the only reason: the role itself is stateless, and the accessor is the API
// on this seam exactly as it is on a store.
//
// The source is held by INTERFACE, not by the concrete wrapper. That is what
// makes uow.IssueReaderSource load-bearing rather than decorative: this call
// site type-checks against the accessor the provider seam publishes, so
// renaming or dropping it is a compile error here.
//
// EITHER WAY it goes out through checkedReader, which is what makes
// handleGetIssue's dereference of the detail view safe by construction again —
// see roles.go.
func (s *Server) reader(r *http.Request) (issueops.Reader, error) {
	if s.provider == nil {
		return checkedReader{inner: s.issueReader}, nil
	}
	var src uow.IssueReaderSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	rd, err := src.IssueReader()
	if err != nil {
		return nil, err
	}
	return checkedReader{inner: rd}, nil
}

// statsReporter returns the guarded summary-statistics surface for one request.
//
// Same two sources as reader() and claimer(), for the same reasons, and held by
// INTERFACE so uow.StatsReporterSource is load-bearing rather than decorative.
// No checked wrapper: issueops.StatsResult carries a VALUE, so there is no
// nil-with-nil-error answer for a handler to dereference. checkedReader exists
// because Reader.Get hands back a pointer.
func (s *Server) statsReporter(r *http.Request) (issueops.StatsReporter, error) {
	if s.provider == nil {
		return s.issueStats, nil
	}
	var src uow.StatsReporterSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.StatsReporter()
}

// cycleDetector returns the guarded cycle-report surface for one request.
//
// Same two sources as reader() and claimer(), for the same reasons, and held by
// INTERFACE so uow.CycleDetectorSource is load-bearing rather than decorative.
// No checked wrapper: this report is a value whose slice a nil-safe range
// walks, so there is no dereference for a misbehaving implementation to turn
// into a panic.
func (s *Server) cycleDetector(r *http.Request) (issueops.CycleDetector, error) {
	if s.provider == nil {
		return s.issueCycles, nil
	}
	var src uow.CycleDetectorSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.CycleDetector()
}

// claimer returns the guarded atomic-claim surface for one request.
//
// It is the write-side twin of reader above, for all the same reasons: the
// configured role on the roles source, and on the provider source one built per
// request so its units of work are timed into THIS request's log line, held by
// INTERFACE so uow.IssueClaimerSource is load-bearing rather than decorative —
// and, from either source, wrapped in checkedClaimer.
func (s *Server) claimer(r *http.Request) (issueops.Claimer, error) {
	if s.provider == nil {
		return checkedClaimer{inner: s.issueClaimer}, nil
	}
	var src uow.IssueClaimerSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	cl, err := src.IssueClaimer()
	if err != nil {
		return nil, err
	}
	return checkedClaimer{inner: cl}, nil
}

// batchCloser returns the many-issue close surface for one request, on the same
// terms as every role above and held by INTERFACE so uow.BatchCloserSource is
// load-bearing rather than decorative.
//
// Wrapped in checkedBatchCloser from either source, and the hazard it folds is
// not the one the other wrappers exist for. Nothing here dereferences the issue
// pointer an outcome carries; what this role owns instead is a POSITIONAL array
// the client reads against its own argument list, and checkedBatchCloser says
// what a miscounted or contentless entry in it costs.
func (s *Server) batchCloser(r *http.Request) (issueops.BatchCloser, error) {
	if s.provider == nil {
		return checkedBatchCloser{inner: s.issueBatchCloser}, nil
	}
	var src uow.BatchCloserSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	closer, err := src.BatchCloser()
	if err != nil {
		return nil, err
	}
	return checkedBatchCloser{inner: closer}, nil
}

// readyClaimer returns the take-ready-work surface for one request.
//
// Built the same two ways as claimer above and for the same reasons, and held
// by INTERFACE so uow.ReadyClaimerSource is load-bearing rather than
// decorative.
//
// IT GOES OUT UNWRAPPED, and the difference from checkedClaimer is the whole
// reason that wrapper exists. That one folds a nil issue because handleClaim
// DEREFERENCES the pointer the role returned; this handler forwards it, and a
// nil is not even a fault here — it is the documented answer for an empty ready
// front. A wrapper would be ceremony that reads like a guarantee.
func (s *Server) readyClaimer(r *http.Request) (issueops.ReadyClaimer, error) {
	if s.provider == nil {
		return s.issueReadyClaimer, nil
	}
	var src uow.ReadyClaimerSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.ReadyClaimer()
}

// releaser returns the claim-release surface for one request.
//
// Built the same two ways as claimer above and for the same reasons: the
// configured role on the roles source, and on the provider source one built per
// request so its units of work are timed into THIS request's log line, held by
// INTERFACE so uow.ReleaserSource is load-bearing rather than decorative — and,
// from either source, wrapped in checkedReleaser, because the handler
// dereferences the pointer the result carries.
func (s *Server) releaser(r *http.Request) (issueops.Releaser, error) {
	if s.provider == nil {
		return checkedReleaser{inner: s.issueReleaser}, nil
	}
	var src uow.ReleaserSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	rel, err := src.Releaser()
	if err != nil {
		return nil, err
	}
	return checkedReleaser{inner: rel}, nil
}

// lifecycle returns the guarded issue-mutation surface for one request.
//
// Built the same two ways as claimer above and for the same reasons: the
// configured role on the roles source, and on the provider source one built per
// request so its units of work are timed into THIS request's log line, held by
// INTERFACE so uow.IssueLifecycleSource is load-bearing rather than decorative
// — and, from either source, wrapped in checkedLifecycle.
func (s *Server) lifecycle(r *http.Request) (issueops.Lifecycle, error) {
	if s.provider == nil {
		return checkedLifecycle{inner: s.issueLifecycle}, nil
	}
	var src uow.IssueLifecycleSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	lc, err := src.IssueLifecycle()
	if err != nil {
		return nil, err
	}
	return checkedLifecycle{inner: lc}, nil
}

// workspaceConfig returns the guarded workspace-settings surface for one
// request.
//
// Same two sources as reader and claimer above, for the same reasons, and held
// by INTERFACE so uow.WorkspaceConfigSource is load-bearing rather than
// decorative. No checked wrapper: both settings handlers read VALUES out of the
// result, so there is no pointer for a caller-supplied role to hand back nil
// in.
func (s *Server) workspaceConfig(r *http.Request) (issueops.WorkspaceConfig, error) {
	if s.provider == nil {
		return s.settings, nil
	}
	var src uow.WorkspaceConfigSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.WorkspaceConfig()
}

// edgeReader returns the guarded stored-edge surface for one request.
//
// Same two sources as reader() and claimer(), for the same reasons, and held by
// INTERFACE so uow.EdgeReaderSource is load-bearing rather than decorative. No
// checked wrapper: this role answers with a VALUE, so no handler dereferences a
// pointer it returned — checkedReader exists for Get alone.
func (s *Server) edgeReader(r *http.Request) (issueops.EdgeReader, error) {
	if s.provider == nil {
		return s.issueEdges, nil
	}
	var src uow.EdgeReaderSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.EdgeReader()
}

// graphCounter returns the edge-count surface for one request, built the same
// two ways as edgeReader above and held by INTERFACE so
// uow.GraphCounterSource is load-bearing rather than decorative.
//
// It goes out UNWRAPPED, for counter's reason: the role answers with a VALUE
// whose slice a nil-safe range walks, so no handler dereferences a pointer it
// returned and a checked wrapper would be ceremony that reads like a guarantee.
func (s *Server) graphCounter(r *http.Request) (issueops.GraphCounter, error) {
	if s.provider == nil {
		return s.issueEdgeCounter, nil
	}
	var src uow.GraphCounterSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.GraphCounter()
}

// relations returns the single-anchor neighbor surface for one request, built
// the same two ways as edgeReader above and held by INTERFACE so
// uow.RelationsSource is load-bearing rather than decorative.
//
// It goes out UNWRAPPED even though the role answers with a slice of POINTERS,
// which is the one place this differs from checkedReader's argument. That
// wrapper exists because handleGetIssue DEREFERENCES the pointer a role handed
// back; here wireRelated drops a nil element the way wireItems and wireEdges
// already do, so there is nothing for a checked wrapper to make safe.
func (s *Server) relations(r *http.Request) (issueops.Relations, error) {
	if s.provider == nil {
		return s.issueRelations, nil
	}
	var src uow.RelationsSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.IssueRelations()
}

// commenter returns the append-one-comment surface for one request, built the
// same two ways as every role above and held by INTERFACE so
// uow.CommenterSource is load-bearing rather than decorative.
//
// It goes out WRAPPED, unlike the reads beside it and for checkedClaimer's
// reason: handleAddComment dereferences the pointer the role answers with, so a
// caller-supplied role that reported success without a row would panic on a live
// server rather than reaching the generic 500 with the fault in the log.
func (s *Server) commenter(r *http.Request) (issueops.Commenter, error) {
	if s.provider == nil {
		return checkedCommenter{inner: s.issueCommenter}, nil
	}
	var src uow.CommenterSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	c, err := src.Commenter()
	if err != nil {
		return nil, err
	}
	return checkedCommenter{inner: c}, nil
}

// blockingAnnotator returns the derived blocking-decoration surface for one
// request, on the same terms as every role above and held by INTERFACE so
// uow.BlockingAnnotatorSource is load-bearing rather than decorative. It goes
// out UNWRAPPED for the reason edgeReader's answer does: this role answers with
// a VALUE, and checkedReader exists for Get alone.
func (s *Server) blockingAnnotator(r *http.Request) (issueops.BlockingAnnotator, error) {
	if s.provider == nil {
		return s.issueBlocking, nil
	}
	var src uow.BlockingAnnotatorSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.BlockingAnnotator()
}

// treeWalker returns the guarded dependency-tree surface for one request.
//
// Built the same two ways as its siblings and for the same reasons, held by
// INTERFACE so uow.TreeWalkerSource is load-bearing rather than decorative. No
// checked wrapper, for the reason cycleDetector gives: this role answers with a
// VALUE whose slice a nil-safe range walks.
func (s *Server) treeWalker(r *http.Request) (issueops.TreeWalker, error) {
	if s.provider == nil {
		return s.issueTree, nil
	}
	var src uow.TreeWalkerSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.TreeWalker()
}

// readyCounter returns the ready-count surface for one request, on the same
// terms as every role above and held by INTERFACE so uow.ReadyCounterSource is
// load-bearing rather than decorative.
//
// It goes out UNWRAPPED. checkedReader and checkedClaimer exist because their
// handlers dereference a POINTER a role returned; CountReady answers with a
// value, so a wrapper would be ceremony that reads like a guarantee.
func (s *Server) readyCounter(r *http.Request) (issueops.ReadyCounter, error) {
	if s.provider == nil {
		return s.issueReadyCounter, nil
	}
	var src uow.ReadyCounterSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.ReadyCounter()
}

// counter returns the issue-count surface for one request, on the same terms as
// readyCounter above and held by INTERFACE so uow.CounterSource is load-bearing
// rather than decorative.
//
// It goes out UNWRAPPED, for readyCounter's reason: both of this role's methods
// answer with a VALUE, so a checked wrapper would be ceremony that reads like a
// guarantee. The one pointer-shaped thing in its result is CountByGroupResult's
// map, and the role promises an empty map rather than nil — a promise the
// handler does not have to trust, because a nil map ranges and marshals as an
// empty object either way.
func (s *Server) counter(r *http.Request) (issueops.Counter, error) {
	if s.provider == nil {
		return s.issueCounter, nil
	}
	var src uow.CounterSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.Counter()
}

// querier returns the boolean-query surface for one request, on the same terms
// as every role above and held by INTERFACE so uow.QuerierSource is
// load-bearing rather than decorative. It goes out UNWRAPPED, like the counter
// and unlike checkedReader: a page is a value carrying a slice, so there is
// nothing for a wrapper to make safe.
func (s *Server) querier(r *http.Request) (issueops.Querier, error) {
	if s.provider == nil {
		return s.issueQuerier, nil
	}
	var src uow.QuerierSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.Querier()
}

// sweeper returns the guarded bulk-clearance surface for one request, on the
// same terms as every role above and held by INTERFACE so uow.SweeperSource is
// load-bearing rather than decorative. It goes out unwrapped: SweepResult is a
// VALUE, so there is no pointer for a caller-supplied role to hand back nil in.
//
// The role this returns is the ONLY thing standing between a POST body and a
// mass delete — the require-a-filter gate, the pinned protection and the
// closed_at recheck are all inside it — which is why the Config field it comes
// from is required rather than optional.
func (s *Server) sweeper(r *http.Request) (issueops.Sweeper, error) {
	if s.provider == nil {
		return s.issueSweeper, nil
	}
	var src uow.SweeperSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.Sweeper()
}

// deleter returns the named-row erasure surface for one request, on the same
// terms as every role above and held by INTERFACE so uow.DeleterSource is
// load-bearing rather than decorative. It goes out unwrapped for the reason the
// sweeper does: DeleteResult is a VALUE.
//
// The role this returns is the only thing standing between a POST body and an
// orphaned dependency graph — the guard, the id resolution and the reference
// rewrite are all inside it — which is why the Config field it comes from is
// required rather than optional.
func (s *Server) deleter(r *http.Request) (issueops.Deleter, error) {
	if s.provider == nil {
		return s.issueDeleter, nil
	}
	var src uow.DeleterSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.Deleter()
}

// batchCreator returns the batch-create surface for one request, on the same
// terms as every role above and held by INTERFACE so uow.BatchCreatorSource is
// load-bearing rather than decorative.
//
// It goes out CHECKED, unlike the ready counter. CreateBatchResult carries a
// slice of POINTERS and the response body carries values, so the handler
// dereferences every one of them — the checkedClaimer hazard, N times over.
// See checkedBatchCreator.
func (s *Server) batchCreator(r *http.Request) (issueops.BatchCreator, error) {
	if s.provider == nil {
		return checkedBatchCreator{inner: s.issueBatchCreator}, nil
	}
	var src uow.BatchCreatorSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	creator, err := src.BatchCreator()
	if err != nil {
		return nil, err
	}
	return checkedBatchCreator{inner: creator}, nil
}

// dependencyEditor returns the guarded dependency-graph write surface for one
// request, on the same terms as every role above and held by INTERFACE so
// uow.DependencyEditorSource is load-bearing rather than decorative.
//
// It goes out UNWRAPPED, like the sweeper and the deleter: both of this role's
// results are VALUES, so no handler dereferences a pointer it returned.
//
// The role this returns owns every refusal the graph can raise — the cycle
// gate, the hierarchy rule, the type conflict and the endpoint existence checks
// — which is why the Config field it comes from is required rather than
// optional.
func (s *Server) dependencyEditor(r *http.Request) (issueops.DependencyEditor, error) {
	if s.provider == nil {
		return s.issueDependencies, nil
	}
	var src uow.DependencyEditorSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.DependencyEditor()
}

// metadataCAS returns the conditional metadata write for one request, on the
// same terms as every role above and held by INTERFACE so
// uow.MetadataCASSource is load-bearing rather than decorative.
//
// It goes out UNWRAPPED: the role's result is a VALUE whose only pointer member
// is an optional raw value the handler passes through without dereferencing.
func (s *Server) metadataCAS(r *http.Request) (issueops.MetadataCAS, error) {
	if s.provider == nil {
		return s.issueMetadataCAS, nil
	}
	var src uow.MetadataCASSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.MetadataCAS()
}

// batchApplier returns the ordered-plan write surface for one request, on the
// same terms as every role above and held by INTERFACE so
// uow.BatchApplierSource is load-bearing rather than decorative.
//
// It goes out UNWRAPPED, like the dependency editor: ApplyBatchResult is a
// VALUE, and the one pointer its items carry — the post-item issue snapshot —
// never reaches the wire, so no handler dereferences anything this role
// returned.
//
// The role owns every refusal this operation can raise: the ref graph, the
// as-modified preconditions, the close policy, the assignee fence and the end
// gate. That is why the Config field it comes from is required rather than
// optional.
func (s *Server) batchApplier(r *http.Request) (issueops.BatchApplier, error) {
	if s.provider == nil {
		return s.issueBatchApplier, nil
	}
	var src uow.BatchApplierSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.BatchApplier()
}

// memories returns the persistent-memory surface for one request, on the same
// terms as every role above and held by INTERFACE so uow.MemoriesSource is
// load-bearing rather than decorative.
//
// It goes out UNWRAPPED: all four of this role's results are VALUES, so no
// handler dereferences a pointer it returned, and a miss is a Found field
// rather than a nil the wire would have to interpret.
func (s *Server) memories(r *http.Request) (memoryops.Memories, error) {
	if s.provider == nil {
		return s.workspaceMemories, nil
	}
	var src uow.MemoriesSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.Memories()
}

// eventsJournalCursor returns the journal read surface for one request, on the
// same terms as every role above and held by INTERFACE so
// uow.EventsJournalCursorSource is load-bearing rather than decorative.
//
// It goes out UNWRAPPED: a page is a value carrying a slice, so there is
// nothing for a checked wrapper to make safe — the querier's argument.
//
// The narrow type is the point of this accessor rather than an accident of it.
// The provider can also hand out uw.EventsJournalUseCase(), which PRUNES; going
// through a source that only publishes storage.EventsJournalCursor is what
// keeps the read-only promise a fact about what the handler is holding.
func (s *Server) eventsJournalCursor(r *http.Request) (storage.EventsJournalCursor, error) {
	if s.provider == nil {
		if s.eventsJournal == nil {
			// Unreachable through the handler, which refuses a disabled
			// workspace before asking for a reader, and Listen refuses an
			// enabled one with no reader. Said out loud rather than returned as
			// a nil interface for the reason WithUOW says its own: a 500 naming
			// the condition beats a panic on a live server if either of those
			// two gates is ever moved.
			return nil, errors.New("httpapi: this server has no events-journal reader; it was configured for a workspace with the journal off")
		}
		return s.eventsJournal, nil
	}
	var src uow.EventsJournalCursorSource = timedProvider{inner: s.provider, rec: requestInfo(r.Context())}
	return src.EventsJournalCursor()
}

// WithUOW runs fn inside one unit of work and guarantees the rollback.
//
// The close context is DETACHED on purpose. Close sends ROLLBACK on the pinned
// connection, and the transaction layer POISONS that connection if the send
// fails (internal/storage/uow/doltserver_tx.go) — go-sql-driver's session reset
// does not clear an open transaction, so a session that may still be in one
// must never go back to the pool. Correctness is therefore safe either way, but
// closing with the request's own canceled context would fail the ROLLBACK
// immediately and burn one pinned session on every client disconnect. Reads
// never commit.
//
// It is provider-only, and says so rather than dereferencing nil: a
// roles-backed server has no unit of work to open, and the roles it does hold
// own their own transactions.
func (s *Server) WithUOW(ctx context.Context, rec *reqInfo, fn func(uow.UnitOfWork) error) error {
	if s.provider == nil {
		return errors.New("httpapi: this server has no unit-of-work provider; it answers from configured issue roles")
	}
	start := time.Now()
	uw, err := s.provider.NewUOW(ctx)
	if rec != nil {
		rec.uowWait = time.Since(start)
	}
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uowCloseTimeout)
		defer cancel()
		uw.Close(closeCtx)
	}()
	return fn(uw)
}

// acquire takes a database slot, or gives up. A timed-out wait is ErrBusy, not
// a request parked for the full deadline and then answered with a
// non-retryable 500.
func (s *Server) acquire(ctx context.Context, rec *reqInfo) (release func(), err error) {
	start := time.Now()
	release = func() { <-s.sem }

	select {
	case s.sem <- struct{}{}:
		rec.semWait = time.Since(start)
		return release, nil
	default:
	}

	timer := time.NewTimer(orDefault(s.semTimeout, semAcquireTimeout))
	defer timer.Stop()
	select {
	case s.sem <- struct{}{}:
		rec.semWait = time.Since(start)
		s.noteSaturation(rec, "acquired")
		return release, nil
	case <-timer.C:
		rec.semWait = time.Since(start)
		s.event("semaphore_timeout", "request_id", rec.id, "wait_ms", millis(rec.semWait),
			"inflight", maxInflight, "conns", s.liveConns.Load())
		return nil, ErrBusy
	case <-ctx.Done():
		// The client hung up, or the request deadline expired, while queued.
		// Still a saturation datapoint: it is the same wedge, observed from a
		// request that did not live long enough to be shed.
		rec.semWait = time.Since(start)
		s.noteSaturation(rec, "abandoned")
		return nil, ctx.Err()
	}
}

// noteSaturation logs a wait that lasted long enough to matter. This is the
// signal that separates "wedged" from "no traffic" at 3 a.m., because /healthz
// stays green either way.
func (s *Server) noteSaturation(rec *reqInfo, outcome string) {
	if rec.semWait < orDefault(s.semWarn, saturationWarn) {
		return
	}
	s.event("semaphore_saturated",
		"request_id", rec.id, "wait_ms", millis(rec.semWait),
		"inflight", maxInflight, "conns", s.liveConns.Load(), "outcome", outcome)
}

func orDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

// handler builds the whole request path: the route table's registrations, the
// catch-all that keeps unrouted paths on the same error shape, and the
// middleware in front of both.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	// Rows carrying a customMethod SHARE a pattern, so they get one
	// registration between them and a dispatcher in front. Collected in table
	// order, which is the order customMethodTarget tries the suffixes in.
	shared := map[string][]route{}
	var sharedOrder []string
	for _, rt := range routeTable {
		if rt.customMethod == "" {
			mux.Handle(rt.method+" "+rt.pattern, s.route(rt))
			continue
		}
		key := rt.method + " " + rt.pattern
		if _, seen := shared[key]; !seen {
			sharedOrder = append(sharedOrder, key)
		}
		shared[key] = append(shared[key], rt)
	}
	for _, key := range sharedOrder {
		mux.Handle(key, s.dispatchCustomMethod(shared[key]))
	}

	// Not an operation and deliberately not in the route table: it exists so
	// that an unrouted path still answers with problem+json rather than
	// net/http's text/plain default, which the document promises for EVERY
	// non-2xx byte. A method mismatch on a known path lands here too and
	// answers 404 rather than 405, because 405 is not in the v0 vocabulary.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, newResult(CodeNotFound, "no such route on this server"))
	}))

	return s.withRequestContext(s.checkHost(mux))
}

// reqInfo is the per-request record the log line is assembled from. Layers fill
// in what they know; the outermost middleware writes it.
type reqInfo struct {
	id      string
	op      string
	status  int
	code    Code
	semWait time.Duration
	uowWait time.Duration
	// refused is the caller-supplied value a middleware turned down: the Host
	// this server does not answer to, or the unrecognized parameter name. It
	// goes on the request line so a refusal is attributable — a rebinding probe
	// that leaves no server-side trace is a control nobody can investigate.
	// logValue quotes it, which is what makes logging attacker-controlled text
	// safe.
	//
	// A pointer, so that the request line carries the field only when there was
	// a refusal: the empty parameter name in `?=1` is a refusal with an empty
	// value, not the absence of one.
	refused *string
}

// refuse records the value a middleware turned down, for the request line.
func (rec *reqInfo) refuse(value string) { rec.refused = &value }

type reqInfoKey struct{}

// requestInfo returns the record for the request in flight. It never returns
// nil: every request goes through withRequestContext, and handing back a
// detached record rather than nil means a mis-wired caller loses a log line
// instead of panicking mid-response.
func requestInfo(ctx context.Context) *reqInfo {
	if rec, ok := ctx.Value(reqInfoKey{}).(*reqInfo); ok {
		return rec
	}
	return &reqInfo{}
}

// withRequestContext assigns the correlation id, applies response-wide
// headers, recovers panics, and writes the one log line per request. It is
// outermost so that a request refused by the Host check is logged like any
// other.
func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &reqInfo{id: s.nextID(), status: http.StatusOK}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, rec))

		// No client or intermediary may cache an answer about live work.
		w.Header().Set("Cache-Control", "no-store")

		sw := &statusWriter{
			ResponseWriter: w,
			rc:             http.NewResponseController(w),
			budget:         orDefault(s.writeStall, writeStallTimeout),
		}
		// Arm before the handler runs: a handler that writes nothing still has a
		// response, written by net/http on the way out.
		sw.extendWriteDeadline(sw.budget)

		start := time.Now()

		// Deferred, so that the two things this middleware promises — an answer
		// in the documented shape, and one log line per request — survive the
		// one failure where correlating them matters most.
		defer func() {
			p := recover()
			if p != nil && p != http.ErrAbortHandler {
				s.panicked(sw, r, rec, p)
			}

			// net/http flushes what is left of the buffered response after the
			// handler returns, so extend once more to cover it. The extension has
			// to outlast the idle timeout too: the deadline stays armed while a
			// keep-alive connection waits for its next request, and net/http
			// answers some requests (a malformed request line, oversized headers)
			// without reaching this middleware — an expired deadline would turn
			// those into a dropped connection. Anything that does reach here
			// re-arms above, before a handler writes a byte.
			sw.extendWriteDeadline(sw.budget + idleTimeout)

			if sw.status != 0 {
				// Still zero means the handler returned without writing
				// anything, in which case net/http has sent the 200 rec already
				// carries.
				rec.status = sw.status
			}

			fields := []any{
				"request_id", rec.id,
				"op", rec.op,
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"code", string(rec.code),
				"duration_ms", millis(time.Since(start)),
				"sem_wait_ms", millis(rec.semWait),
				"uow_ms", millis(rec.uowWait),
				// The connection gauge belongs on the busiest line in the log:
				// it is the only place an operator watches it climb toward the
				// cap. remote_addr answers "which client", which on loopback
				// means "which local process" via the port.
				"conns", s.liveConns.Load(),
				"remote_addr", r.RemoteAddr,
			}
			if rec.refused != nil {
				fields = append(fields, "refused", *rec.refused)
			}
			s.event("request", fields...)

			if p == http.ErrAbortHandler {
				// net/http's documented "abandon this response silently" signal.
				// It gets its log line like every other request, and then it gets
				// the abort it asked for.
				panic(p)
			}
		}()

		next.ServeHTTP(sw, r)
	})
}

// panicked gives a panicking handler the same shape as every other failure: one
// problem+json response and one log line, both carrying the request id.
//
// Without it the panic reaches net/http's per-connection recover, which prints
// an unstructured stack trace to stderr with nothing on it to tie to a client
// report, drops the connection with no body at all, and skips the request line —
// so the one class of failure where correlation matters most is the one class
// that has none. The panic text stays out of the response for the same reason
// every other 5xx detail does.
func (s *Server) panicked(sw *statusWriter, r *http.Request, rec *reqInfo, p any) {
	rec.code = CodeInternal
	s.event("panic",
		"request_id", rec.id,
		"op", rec.op,
		"method", r.Method,
		"path", r.URL.Path,
		"error", fmt.Sprint(p),
		"stack", string(debug.Stack()),
	)
	if sw.status != 0 {
		// The response is already on the wire; a truncated body is all the
		// client can be told, and writing a second header would only add a
		// superfluous-WriteHeader line to the log.
		return
	}
	s.fail(sw, r, newResult(CodeInternal, ""))
}

// checkHost is the DNS-rebinding defense. An unauthenticated service on
// loopback is reachable from any browser on the host; a page that re-resolves
// its own name to 127.0.0.1 issues requests the browser treats as same-origin,
// so no CORS rule stops them. What the browser does preserve is the attacker's
// hostname in Host, which is what this rejects.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hosts.allows(r.Host) {
			requestInfo(r.Context()).refuse(r.Host)
			s.fail(w, r, InvalidArgument("Host", ReasonInvalidValue,
				"Host header is not one this server answers to"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// dispatchCustomMethod is the one registration the single-resource custom
// methods share. It splits the trailing `:verb` off the matched segment, hands
// the request to the row that claims it, and leaves the id where that row's
// handler reads it.
//
// The split happens BEFORE s.route, which is what makes an unrouted suffix cost
// nothing: it takes no database slot and books no operation on the request
// line, exactly as the catch-all's 404 does. Answering it from inside a row's
// handler — where the claim answered it while it was the only POST here — would
// attribute every probe of this prefix to whichever operation happened to be
// first in the table.
func (s *Server) dispatchCustomMethod(rows []route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, id, res := customMethodTarget(rows, r.PathValue(customMethodPathValue))
		if res != nil {
			s.fail(w, r, *res)
			return
		}
		r.SetPathValue(customMethodIDValue, id)
		s.route(rt).ServeHTTP(w, r)
	})
}

// closeStreams signals every streaming handler that this server is shutting
// down. Idempotent: http.Server calls its registered hooks once per Shutdown,
// and a second Shutdown must not panic on an already-closed channel.
func (s *Server) closeStreams() {
	s.closingOnce.Do(func() { close(s.closing) })
}

// route wraps one operation with the limits that apply to it: the per-request
// deadline, the bearer credential unless the operation is exempt, the
// Bd-Project-Id stamp check unless the operation is exempt, and — unless the
// operation is exempt — a database slot.
//
// The credential check runs BEFORE the semaphore, which is the load-bearing
// ordering: a storm of refused requests then costs one SHA-256 each and can
// never occupy the slots, or the SQL connections pinned to them, that
// authenticated clients are waiting for. It runs inside withRequestContext, so
// a 401 gets a request id and a request log line like every other refusal.
//
// The stamp check runs AFTER the credential and before the semaphore: the
// project-mismatch refusal is the one that discloses this server's own project
// id (server_project_id), so it must sit behind the authentication gate — an
// unauthenticated caller is turned away by the 401 before the stamp is ever
// compared, and so learns nothing about the workspace's identity.
func (s *Server) route(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := requestInfo(r.Context())
		rec.op = rt.op

		// A STREAMING OPERATION GETS NO DEADLINE. For every other row this is
		// the backstop that stops a request from holding resources forever; on a
		// held-open response it would do nothing but sever the stream at sixty
		// seconds. What replaces it is per-read: streamEvents holds no slot
		// between passes and bounds each read on its own.
		if !rt.streaming {
			ctx, cancel := context.WithTimeout(r.Context(), requestDeadline)
			defer cancel()
			r = r.WithContext(ctx)
		}

		if !rt.authExempt && s.auth != nil && !s.authorize(w, r, rec) {
			return
		}

		// Identity before resources: a stamp for the wrong workspace is turned
		// away before it can buy a database slot or open a unit of work, so a
		// misdirected read or write costs nothing and mutates nothing. The two
		// exempt routes (liveness, identity handshake) skip it. It runs after
		// the credential check so the server_project_id it discloses stays
		// behind the authentication gate.
		if !rt.projectExempt {
			if res := s.checkProjectStamp(r); res != nil {
				s.fail(w, r, *res)
				return
			}
		}

		if !rt.bypassSemaphore {
			release, err := s.acquire(r.Context(), rec)
			if err != nil {
				s.failErr(w, r, err)
				return
			}
			defer release()
		}

		rt.handler(s, w, r)
	})
}

// authorize verifies the request's bearer credential, writing the 401 itself
// and reporting whether the handler may run.
//
// The presented credential appears in NO log field and NO response byte. It is
// deliberately not recorded through rec.refuse, which is defined as an echoed
// CALLER VALUE and goes on the request line: a Host header or a parameter name
// is attacker-controlled text worth attributing, and a token is a secret. What
// the log gets instead is the reason — which of the three client mistakes it
// was — and the request id that ties it to the response.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, rec *reqInfo) bool {
	token, reason := bearerCredential(r.Header.Get("Authorization"))
	if reason == "" {
		ok, reloadErr := s.auth.Verify(token)
		if reloadErr != nil {
			// A reload that failed leaves the last-good token set in force, so
			// this is not a refusal on its own — but it means the file the
			// operator is rotating is unreadable, and nothing else would say so.
			s.event("auth_reload_error", "request_id", rec.id, "error", reloadErr.Error())
		}
		if ok {
			return true
		}
		reason = "unknown_token"
	}

	s.event("auth_refused", "request_id", rec.id, "op", rec.op,
		"reason", reason, "remote_addr", r.RemoteAddr)
	s.fail(w, r, newResult(CodeUnauthenticated, ""))
	return false
}

// bearerCredential extracts the token from an Authorization header value. It
// returns a non-empty reason instead when there is nothing to verify, so the
// log can separate a misconfigured client from a wrong or stale token.
//
// The scheme is matched case-insensitively, which RFC 9110 requires.
func bearerCredential(header string) (token, reason string) {
	if strings.TrimSpace(header) == "" {
		return "", "missing"
	}
	rest, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		scheme, tail, split := strings.Cut(header, " ")
		if !split || !strings.EqualFold(scheme, "Bearer") {
			return "", "malformed"
		}
		rest = tail
	}
	if token = strings.TrimSpace(rest); token == "" {
		return "", "malformed"
	}
	return token, ""
}

// authLabel describes the credential posture for the startup line. It names the
// token FILE, never a token: the path is configuration an operator already
// knows, and the contents are the secret.
func (s *Server) authLabel() string {
	if s.auth == nil {
		return "none"
	}
	return "bearer (" + s.auth.path + ")"
}

// checkProjectStamp enforces per-request workspace identity. A client that
// stamps a request with its intended workspace's project id in the
// Bd-Project-Id header is asserting "I mean to be talking to THIS workspace"; if
// the id it names is not the one this server serves, the request is refused
// before it can read or write the wrong workspace. It returns nil when the
// request may proceed, or the 400 to write.
//
// The comparison is LITERAL, and a server whose own project id is empty refuses
// any non-empty stamp: it cannot prove it is the workspace the client named, so
// it does not answer as if it were. That mirrors the identity handshake — a
// server advertises the id it can assert, and asserts nothing when it has none.
//
// Two paths return nil. An ABSENT header is the backward-compatible one: an
// older client never sends it, and enforcement triggers only when the header
// arrives, so this adds no precondition to any request already in the field. A
// stamp equal to the server's own project id is a match.
//
// The refusal is recorded on the request line like every other middleware
// refusal, so a client persistently addressing the wrong server is attributable
// on loopback down to the local process.
func (s *Server) checkProjectStamp(r *http.Request) *Result {
	stamp := r.Header.Get(ProjectIDHeader)
	if stamp == "" {
		return nil
	}
	if stamp == s.ctxBody.ProjectId {
		return nil
	}
	requestInfo(r.Context()).refuse(stamp)
	res := ProjectMismatch(stamp, s.ctxBody.ProjectId)
	return &res
}

// fail writes a problem response and records what it was for the log line.
// Every non-2xx byte this server emits goes through here or through
// handleNotImplemented.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, res Result) {
	rec := requestInfo(r.Context())
	rec.code = Code(res.Problem.Code)
	Write(w, res.WithRequestID(rec.id))
}

// failErr maps an error from the storage seam and logs the error text. On a 5xx
// that text goes to the log and NOWHERE else: driver and dial errors routinely
// embed the DSN, and the response detail is a fixed string per code. The
// request_id in both places is what reconnects them.
func (s *Server) failErr(w http.ResponseWriter, r *http.Request, err error) {
	rec := requestInfo(r.Context())
	res := ClassifyError(err)
	s.fail(w, r, res)

	// A client that hung up — while queued for a slot, or mid unit of work — is
	// not a server fault, and this is the moment it would be counted as one:
	// context.Canceled has nowhere better to go than the generic 500, and every
	// >=500 emits request_error. On a saturated server, which is exactly when
	// clients time out and disconnect, that turns impatient callers into a spike
	// in the one signal an operator alerts on. The status stays as classified
	// (it is written to a socket nobody is reading either way); only the
	// accounting changes.
	//
	// An EXPIRED request deadline is a different statement and keeps the 500:
	// nothing about it says the client left.
	if errors.Is(err, context.Canceled) {
		rec.code = codeClientClosed
		return
	}
	if res.Problem.Status >= 500 {
		s.event("request_error", "request_id", rec.id, "error", err.Error())
	}
}

// writeJSON emits a success body. The status is always 200: every 2xx on this
// surface is a 200, and every non-2xx byte goes through Write as problem+json
// instead — so a status parameter here would only ever be a way to write one
// that the document does not describe.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// statusWriter records the status for the log line and bounds how long any one
// write may stall. It intentionally does not buffer the body: an unlimited read
// must stream.
type statusWriter struct {
	http.ResponseWriter
	rc     *http.ResponseController
	budget time.Duration
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.extendWriteDeadline(w.budget)
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.extendWriteDeadline(w.budget)
	return w.ResponseWriter.Write(b)
}

// extendWriteDeadline rolls the connection's write deadline d into the future.
// Rolling it before every write bounds each write rather than the transfer: a
// client that keeps reading streams a body of any size, while one that stops
// reading fails its handler within d — which is what lets the deferred
// semaphore release and unit-of-work rollback actually run.
//
// SetWriteDeadline is unsupported on a ResponseWriter with no connection under
// it (httptest's recorder), where there is nothing to stall; the error is
// dropped for that reason and no other.
func (w *statusWriter) extendWriteDeadline(d time.Duration) {
	if w.rc == nil || d <= 0 {
		return
	}
	_ = w.rc.SetWriteDeadline(time.Now().Add(d))
}

// Unwrap keeps http.ResponseController working through the wrapper, so a
// handler that needs to flush a large streamed page still can.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// hostPolicy is the set of Host header values this server answers to. It is
// data rather than a closure so the startup line can state the whole policy,
// and so the wildcard case below is a visible rule instead of an absence.
type hostPolicy struct {
	// ips are the numeric addresses allowed, matched with net.IP.Equal so every
	// spelling of one address matches: [0:0:0:0:0:0:0:1] and [::ffff:127.0.0.1]
	// are the same hosts as ::1 and 127.0.0.1, and a client that spells one of
	// them the long way is not an attacker.
	ips []net.IP
	// names are the allowed non-numeric Host values, lowercased. "localhost"
	// is always there; an operator may enumerate more with --allowed-host, and
	// nothing else can add one. Matching is EXACT — no wildcard and no suffix
	// syntax — so the allowlist is precisely what was enumerated.
	names map[string]bool
	// anyIP additionally allows ANY numeric Host literal. Only a wildcard bind
	// sets it; see newHostPolicy for why that is still a rebinding defense.
	anyIP bool
}

// newHostPolicy returns the Host policy implied by a bind address.
//
// The loopback spellings are always allowed, and the bind's own address is too
// — including an alternate loopback bind like 127.0.0.2, whose clients dial
// exactly that address and would otherwise be refused by the defense meant to
// protect them.
//
// A WILDCARD bind (0.0.0.0, ::) has no single configured address to allow, so
// it allows any numeric IP literal instead — and still refuses foreign DNS
// names, which is the whole defense. A rebound page cannot produce an IP-literal
// Host: the browser sends the hostname from the attacker's URL, and fetching an
// IP URL directly is a direct connection, which is the exposure the operator
// accepted when they passed --allow-non-loopback. Disabling the check outright
// would instead surrender the defense on the serving host's own loopback
// interface, which is rebinding's canonical target, and on every LAN browser
// behind a firewall the attacker cannot otherwise reach.
// EXTRA is the operator's enumerated additions (--allowed-host). A deployment
// where clients dial a service DNS name is refused by the policy above on every
// single request, so without this the server is unreachable rather than
// protected. Admitting a name the operator named does not weaken the defense
// the check exists for: a rebound page still cannot make a browser send that
// Host to 127.0.0.1, and the in-cluster clients that do send it are not
// browsers. Numeric values land in ips, so a pod IP can be enumerated too.
func newHostPolicy(bind net.IP, extra []string) hostPolicy {
	p := hostPolicy{
		ips:   []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		names: map[string]bool{"localhost": true},
		anyIP: bind.IsUnspecified(),
	}
	if !p.anyIP && !containsIP(p.ips, bind) {
		p.ips = append(p.ips, bind)
	}
	for _, host := range extra {
		h := hostOnly(host)
		if ip := net.ParseIP(h); ip != nil {
			if !containsIP(p.ips, ip) {
				p.ips = append(p.ips, ip)
			}
			continue
		}
		p.names[h] = true
	}
	return p
}

// ValidateAllowedHost refuses an allowlist entry that is not a bare host.
//
// The Host header's port is stripped before matching (hostOnly), so an entry
// carrying one would silently never match — and an operator who wrote it would
// reasonably read the startup line as proof that it does. A URL, a path or
// embedded whitespace is the same mistake in a louder form.
func ValidateAllowedHost(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("--allowed-host is empty; pass the Host header value clients send, such as bd-myproject.beads.svc.cluster.local")
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("--allowed-host %q contains whitespace; it must be a bare host name or IP", v)
	}
	if strings.ContainsAny(v, "/@") {
		return fmt.Errorf("--allowed-host %q looks like a URL; pass just the host, with no scheme and no path", v)
	}
	// An IPv6 address is spelled in brackets in a Host header, so an operator
	// copying one off the wire types it that way. hostOnly strips them before
	// matching, so the entry works; refusing it here — with a message about a
	// port it does not have — would be the validation lying about the policy.
	if net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")) != nil {
		return nil
	}
	if strings.Contains(v, ":") {
		return fmt.Errorf("--allowed-host %q carries a port; the port is stripped from a request's Host before matching, so an entry with one could never match", v)
	}
	return nil
}

// allows reports whether a Host header value is one this server answers to.
func (p hostPolicy) allows(host string) bool {
	h := hostOnly(host)
	if p.names[h] {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return p.anyIP || containsIP(p.ips, ip)
}

// label renders the policy for the startup line, so an operator can read what
// this server will answer to without deducing it from the bind address.
func (p hostPolicy) label() string {
	parts := make([]string, 0, len(p.ips)+len(p.names)+1)
	for _, ip := range p.ips {
		parts = append(parts, ip.String())
	}
	parts = append(parts, slices.Sorted(maps.Keys(p.names))...)
	if p.anyIP {
		parts = append(parts, "any-ip-literal (wildcard bind)")
	}
	return strings.Join(parts, ",")
}

func containsIP(ips []net.IP, want net.IP) bool {
	return slices.ContainsFunc(ips, want.Equal)
}

// hostOnly strips the port and any IPv6 brackets from a Host header value.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return strings.ToLower(host)
}

// newIDPrefix draws one random prefix per process so ids from two servers, or
// from two runs, never collide in a shared log.
func newIDPrefix() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("httpapi: request id seed: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Server) nextID() string {
	return fmt.Sprintf("%s-%06d", s.idPrefix, s.idSeq.Add(1))
}

func (s *Server) logStartup() {
	s.event("startup",
		"addr", s.Addr(),
		"mode", s.cfg.Mode,
		"db", s.dbSource(),
		"workspace", s.cfg.Workspace.RepoRoot,
		"beads_dir", s.cfg.Workspace.BeadsDir,
		"database", s.cfg.Workspace.Database,
		"host_allowlist", s.hosts.label(),
		"capabilities", strings.Join(s.ctxBody.Capabilities, ","),
		// Whether this server requires a credential is the first thing an
		// operator checks after a deploy, and the last thing they should have
		// to infer from the absence of a flag in a process listing.
		"auth", s.authLabel(),
	)

	limits := []any{
		"max_inflight", maxInflight,
		"max_conns", maxConns,
		"sem_wait", semAcquireTimeout.String(),
		"deadline", requestDeadline.String(),
	}
	// The pool bounds are this server's, applied to the provider above. On the
	// roles source there is no pool here to bound, and printing the numbers
	// anyway would report limits nothing enforces.
	if s.provider != nil {
		limits = append(limits,
			"pool_max_open", servePoolLimits.MaxOpenConns,
			"pool_max_idle", servePoolLimits.MaxIdleConns,
			"pool_idle_time", servePoolLimits.ConnMaxIdleTime.String(),
			"pool_lifetime", servePoolLimits.ConnMaxLifetime.String(),
		)
	}
	s.event("limits", limits...)
}

// dbSource names which database source this server was built from, for the
// startup line.
//
// It is there so uow_ms is attributable. That field means "how long this
// request spent OBTAINING units of work", and a roles-backed server obtains
// none — so every one of its request lines reads uow_ms=0.000, which is the
// true value and is indistinguishable, on its own, from instrumentation that
// broke. This is the line that tells them apart.
func (s *Server) dbSource() string {
	if s.provider != nil {
		return "provider"
	}
	return "roles"
}

// event writes one structured stderr line. Values are quoted when they are not
// bare tokens, so a path or an error message can never inject a field — or a
// whole line — into the log.
func (s *Server) event(name string, kv ...any) {
	var b strings.Builder
	b.WriteString("event=")
	b.WriteString(name)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(logValue(kv[i+1]))
	}
	s.log.Print(b.String())
}

// logValue renders one value of the k=v request line, quoting anything that
// would disturb either the line or the terminal reading it.
//
// Two audiences, two rules. The k=v framing needs space, '"' and '=' quoted, or
// a caller-supplied value forges fields and whole lines. The operator's console
// needs every CONTROL character quoted, C1 included: an unquoted U+009B is a
// CSI introducer, so a refusal recorded from a request body member name or a
// Content-Type header would paint the terminal of whoever tails the log. Bytes
// 0x80-0xFF are legal obs-text in an HTTP/1 field value and arrive here as
// invalid UTF-8 rather than as runes, so validity is checked separately —
// ContainsFunc would see only U+FFFD, which is not a control character.
func logValue(v any) string {
	str, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	if str == "" {
		return `""`
	}
	if !utf8.ValidString(str) || strings.ContainsFunc(str, func(r rune) bool {
		return r <= ' ' || r == '"' || r == '=' || isControlChar(r)
	}) {
		return strconv.Quote(str)
	}
	return str
}

func millis(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 3, 64)
}
