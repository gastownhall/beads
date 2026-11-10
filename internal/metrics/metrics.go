package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
)

func Enabled() bool {
	return enabled
}

func Endpoint() string {
	return endpoint
}

func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dataDirName, "eventsData"), nil
}

func Init(version string, enable bool, metricsEndpoint string) (func(context.Context), error) {
	enabled = enable
	endpoint = metricsEndpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	var emitter eventkit.Emitter = eventkit.NullEmitter{}
	// The distinct ID is resolved only on the enabled path: computing it can
	// fork a platform probe (see cachedMachineID), and a disabled collector
	// never emits an event that would carry it. The placeholder below is inert
	// — NullEmitter drops everything and WithDisabled gates emission anyway.
	distinctID := "disabled"
	if enabled {
		dir, err := DataDir()
		if err != nil {
			return func(context.Context) {}, fmt.Errorf("metrics: resolve data dir: %w", err)
		}
		fe, err := eventkit.NewFileEmitter(dir)
		if err != nil {
			return func(context.Context) {}, fmt.Errorf("metrics: file emitter: %w", err)
		}
		emitter = fe
		distinctID = cachedMachineID(AppName)
	}

	c := eventkit.NewCollector(emitter,
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
// RunE/ExecuteC path. When metrics are disabled the collector half is inert
// (NullEmitter), but the spawn half still runs: a leftover queue from a
// previously-enabled configuration is drained by prune-only send-metrics
// children (GH#5712). The BD_IS_FLUSHER guard in MaybeSpawnFlusher keeps it
// from recursing.
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
