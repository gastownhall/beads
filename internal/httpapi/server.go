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
	// AllowNonLoopback permits a bind beyond loopback. v0 has no
	// authentication and no TLS, so this is an operator decision that is never
	// taken by default.
	AllowNonLoopback bool
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
	Reader            issueops.Reader
	Claimer           issueops.Claimer
	Settings          issueops.WorkspaceConfig
	Stats             issueops.StatsReporter
	CycleDetector     issueops.CycleDetector
	EdgeReader        issueops.EdgeReader
	BlockingAnnotator issueops.BlockingAnnotator
	TreeWalker        issueops.TreeWalker
	ReadyCounter      issueops.ReadyCounter
	Querier           issueops.Querier
	// Sweeper is the DESTRUCTIVE one, required on the same terms as every other
	// role rather than opt-in: whether this build erases beads is a decision
	// for the operator who chose to run bd serve, not a consequence of whether
	// a caller remembered a field.
	Sweeper issueops.Sweeper
	// Deleter is the OTHER destructive one, required for the same reason.
	Deleter      issueops.Deleter
	BatchCreator issueops.BatchCreator
	// Memories is the workspace's persistent memory plane, and the one field
	// here that is not an issueops role: memories are user data riding in the
	// config table under their own merge class, not settings, so they have
	// their own leaf package. Required on the same terms as every field above —
	// a partial set is refused, so the field and the operations that reach it
	// land together.
	Memories memoryops.Memories
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
	settings          issueops.WorkspaceConfig
	issueStats        issueops.StatsReporter
	issueCycles       issueops.CycleDetector
	issueEdges        issueops.EdgeReader
	issueBlocking     issueops.BlockingAnnotator
	issueTree         issueops.TreeWalker
	issueReadyCounter issueops.ReadyCounter
	issueQuerier      issueops.Querier
	issueSweeper      issueops.Sweeper
	issueDeleter      issueops.Deleter
	issueBatchCreator issueops.BatchCreator
	workspaceMemories memoryops.Memories

	listener net.Listener
	http     *http.Server

	// sem bounds handlers that touch the database. Buffered channel rather
	// than sync.Semaphore so the acquisition can select on a timer.
	sem chan struct{}
	// semTimeout, semWarn and writeStall default to the constants above. They
	// are fields rather than constants at the point of use so the queueing and
	// stalled-write behavior can be exercised in milliseconds instead of tens of
	// seconds.
	semTimeout time.Duration
	semWarn    time.Duration
	writeStall time.Duration

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
		return nil, fmt.Errorf("--addr %q binds beyond loopback; bd serve has no authentication, so this requires --allow-non-loopback", addr)
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
		settings:          cfg.Settings,
		issueStats:        cfg.Stats,
		issueCycles:       cfg.CycleDetector,
		issueEdges:        cfg.EdgeReader,
		issueBlocking:     cfg.BlockingAnnotator,
		issueTree:         cfg.TreeWalker,
		issueReadyCounter: cfg.ReadyCounter,
		issueQuerier:      cfg.Querier,
		issueSweeper:      cfg.Sweeper,
		issueDeleter:      cfg.Deleter,
		issueBatchCreator: cfg.BatchCreator,
		workspaceMemories: cfg.Memories,

		sem:        make(chan struct{}, maxInflight),
		semTimeout: semAcquireTimeout,
		semWarn:    saturationWarn,

		log:      log.New(cfg.Stderr, "bd serve: ", log.LstdFlags|log.LUTC),
		stdout:   cfg.Stdout,
		ctxBody:  contextResponse(cfg.Workspace, cfg.SchemaVersion, Capabilities()),
		hosts:    newHostPolicy(ip),
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
func sourceRoles(cfg Config) []any {
	return []any{cfg.Reader, cfg.Claimer, cfg.Settings, cfg.Stats, cfg.CycleDetector, cfg.EdgeReader, cfg.BlockingAnnotator, cfg.TreeWalker, cfg.ReadyCounter, cfg.Querier, cfg.Sweeper, cfg.Deleter, cfg.BatchCreator, cfg.Memories}
}

// roleSourceNames spells sourceRoles for the refusal message, in the same
// order, so a caller reading the error learns the whole set it must pass.
const roleSourceNames = "Reader, Claimer, Settings, Stats, CycleDetector, EdgeReader, BlockingAnnotator, TreeWalker, ReadyCounter, Querier, Sweeper, Deleter, BatchCreator and Memories"

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
	case cfg.Provider != nil && anyRoleSet(cfg):
		return errors.New("httpapi: both a unit-of-work provider and issue roles were set; pass exactly one database source")
	case cfg.Provider == nil && !everyRoleSet(cfg):
		return errors.New("httpapi: no database source: set Provider, or " + roleSourceNames + " together")
	case anyRoleFiresHooks(cfg):
		return errors.New("httpapi: a configured role fires this workspace's hooks; " +
			"this server does not run hooks, so take the roles from the store beneath the hook decorator " +
			"((*storage.HookFiringStore).Unwrap)")
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
	for _, rt := range routeTable {
		mux.Handle(rt.method+" "+rt.pattern, s.route(rt))
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

// route wraps one operation with the limits that apply to it: the per-request
// deadline, and — unless the operation is exempt — a database slot.
func (s *Server) route(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := requestInfo(r.Context())
		rec.op = rt.op

		ctx, cancel := context.WithTimeout(r.Context(), requestDeadline)
		defer cancel()
		r = r.WithContext(ctx)

		if !rt.bypassSemaphore {
			release, err := s.acquire(ctx, rec)
			if err != nil {
				s.failErr(w, r, err)
				return
			}
			defer release()
		}

		rt.handler(s, w, r)
	})
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
	// names are the allowed non-numeric Host values, lowercased. There is
	// exactly one, "localhost", and no mechanism to add another: a DNS name in
	// a Host header is precisely what the rebinding attack carries.
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
func newHostPolicy(bind net.IP) hostPolicy {
	p := hostPolicy{
		ips:   []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		names: map[string]bool{"localhost": true},
		anyIP: bind.IsUnspecified(),
	}
	if !p.anyIP && !containsIP(p.ips, bind) {
		p.ips = append(p.ips, bind)
	}
	return p
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
