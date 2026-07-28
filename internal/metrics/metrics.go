package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dolthub/eventkit"
)

const (
	AppName     = "beads"
	dataDirName = ".beads"

	EnvDisableMetrics    = "BD_DISABLE_METRICS"
	EnvDisableEventFlush = "BD_DISABLE_EVENT_FLUSH"
	EnvDoNotTrack        = "DO_NOT_TRACK"

	DefaultEndpoint = "https://gastownhall-eventsapi.com/mp/collect"

	// queuedEventExt is the extension the eventkit FileEmitter gives queued
	// event batches in DataDir. Re-exported here (this file holds the fenced
	// eventkit import — see computeMachineID) so spawn.go's pending-events
	// check can recognize them.
	queuedEventExt = eventkit.DefaultFileExt
)

var (
	enabled  bool
	endpoint string

	switchEmitter = &switchableEmitter{current: eventkit.NullEmitter{}}
)

// switchableEmitter lets the collector's destination change after construction:
// eventkit.Collector bakes its emitter into the sendingThread with no way to
// replace it, so Init wires the collector to this wrapper and AttachFileEmitter
// points it at the data dir once that is known.
//
// It builds the file emitter lazily, on the first event needing a write:
// NewFileEmitter calls MkdirAll, so building it eagerly would materialize a
// queue directory for commands that emit nothing.
type switchableEmitter struct {
	mu      sync.Mutex
	current eventkit.Emitter
	dir     string
}

// useNull drops back to the no-op emitter, discarding any armed dir.
func (s *switchableEmitter) useNull() {
	s.mu.Lock()
	s.current = eventkit.NullEmitter{}
	s.dir = ""
	s.mu.Unlock()
}

// useDir arms the file emitter for dir without touching disk.
func (s *switchableEmitter) useDir(dir string) {
	s.mu.Lock()
	s.current = nil
	s.dir = dir
	s.mu.Unlock()
}

// attachedDir reports the armed data dir, or "" if the emitter is still the
// no-op one.
func (s *switchableEmitter) attachedDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

func (s *switchableEmitter) Send(ctx context.Context, req *eventkit.LogEventsRequest) error {
	s.mu.Lock()
	if s.current == nil {
		fe, err := eventkit.NewFileEmitter(s.dir)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("metrics: file emitter: %w", err)
		}
		s.current = fe
	}
	e := s.current
	s.mu.Unlock()
	return e.Send(ctx, req)
}

func Enabled() bool {
	return enabled
}

func Endpoint() string {
	return endpoint
}

// DataDir returns the on-disk telemetry queue directory, under the current
// BEADS_DIR or $HOME/.beads when unset. Call it only after the workspace has
// been selected (applyChangeDirSelection in cmd/bd); earlier, BEADS_DIR may
// still hold the ambient value rather than bd's resolved workspace.
//
// A BEADS_DIR that does not exist yet is ignored in favor of the home queue:
// telemetry may write into a workspace, but must never be the thing that brings
// one into being (a rejected init would otherwise leave a .beads behind).
func DataDir() (string, error) {
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		if _, err := os.Stat(beadsDir); err == nil {
			return filepath.Join(beadsDir, "eventsData"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dataDirName, "eventsData"), nil
}

// Init registers the global collector wired to a no-op emitter, so events
// emitted before the workspace is known are buffered rather than written to the
// wrong directory. Call AttachFileEmitter once it is resolved.
func Init(version string, enable bool, metricsEndpoint string) (func(context.Context), error) {
	enabled = enable
	endpoint = metricsEndpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	switchEmitter.useNull()
	// The distinct ID is resolved only on the enabled path: computing it can
	// fork a platform probe (see cachedMachineID), and a disabled collector
	// never emits an event that would carry it. The placeholder below is inert
	// — NullEmitter drops everything and WithDisabled gates emission anyway.
	distinctID := "disabled"
	if enabled {
		distinctID = cachedMachineID(AppName)
	}

	c := eventkit.NewCollector(switchEmitter,
		eventkit.WithDistinctID(distinctID),
		eventkit.WithAppName(AppName),
		eventkit.WithAppVersion(version),
		eventkit.WithDisabled(func() bool { return !enabled }),
	)
	eventkit.SetGlobal(c)

	return func(ctx context.Context) {
		_ = c.Close(ctx)
	}, nil
}

// AttachFileEmitter arms the on-disk emitter for dataDir, so events queued since
// Init start reaching disk. dataDir is passed in rather than read here so
// resolution stays with the caller, and the directory is not created until there
// is an event to write. No-op when disabled.
func AttachFileEmitter(dataDir string) error {
	if !enabled {
		return nil
	}
	if dataDir == "" {
		return fmt.Errorf("metrics: empty data dir")
	}
	switchEmitter.useDir(dataDir)
	return nil
}

func Global() *eventkit.Collector {
	return eventkit.Global()
}

// computeMachineID is the raw (slow) platform machine-id probe. It lives here
// rather than in machineid.go because eventkit imports are depguard-fenced to
// this file and flusher.go (.golangci.yml dolt-storage-boundary). Callers want
// cachedMachineID, which pays this cost at most once per machine.
func computeMachineID(appName string) string {
	return eventkit.MachineID(appName)
}

// closeFlushTimeout bounds how long CloseAndFlush waits for the collector to
// write queued events before detaching the uploader; it mirrors the budget
// main() has always used for its post-command metrics tail.
const closeFlushTimeout = 500 * time.Millisecond

// CloseAndFlush finalizes any queued events on the global collector (bounded by
// closeFlushTimeout) and then detaches the background flusher. It is the single
// metrics shutdown path shared by main()'s normal post-Execute tail and the
// reachable os.Exit guards (CheckReadonly and the pre-run gates in main), so
// events already queued earlier in this run are still written to disk and
// scheduled for upload even when a command exits without returning through the
// RunE/ExecuteC path. It is a no-op when metrics are disabled or uninitialized,
// and the BD_IS_FLUSHER guard in MaybeSpawnFlusher keeps it from recursing.
func CloseAndFlush() {
	if c := Global(); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), closeFlushTimeout)
		_ = c.Close(ctx)
		cancel()
	}
	MaybeSpawnFlusher()
}

func NewCommandEvent(command string) *eventkit.Event {
	// A telemetry helper must never crash a real command: fall back to a
	// placeholder rather than panicking on an empty command name.
	if command == "" {
		command = "unknown"
	}
	evt := eventkit.NewEvent("cli_command")
	evt.SetAttribute("command", command)
	return evt
}
