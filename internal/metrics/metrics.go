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
	}

	c := eventkit.NewCollector(emitter,
		eventkit.WithDistinctID(eventkit.MachineID(AppName)),
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
