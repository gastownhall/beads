package metrics

import (
	"os"
	"os/exec"
	"strings"
)

const SendMetricsSubcommand = "send-metrics"

// EnvIsFlusher marks the detached send-metrics child process. The parent sets it
// on the child's environment and MaybeSpawnFlusher refuses to spawn when it is
// set, so the flusher can never spawn another flusher (an unbounded process
// chain). This structural guard does not depend on send-metrics calling os.Exit
// before control returns to main()'s spawn tail.
const EnvIsFlusher = "BD_IS_FLUSHER"

func MaybeSpawnFlusher() {
	if os.Getenv(EnvIsFlusher) == "1" {
		return
	}
	if !Enabled() || flushDisabledByEnv() {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, SendMetricsSubcommand)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Hand the child an explicit environment rather than letting it inherit ours
	// wholesale. By the time we spawn, the parent command may have loaded a
	// project .beads/.env that set BEADS_METRICS_ENDPOINT; an inherited value
	// would let a hostile repository redirect where the user's telemetry is
	// uploaded. Pin the endpoint to the value the parent already resolved from
	// env + user-global config (see resolveMetricsEndpoint in cmd/bd) and mark
	// the child as the flusher so it cannot recurse.
	cmd.Env = flusherChildEnv(os.Environ(), Endpoint())
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

// flusherChildEnv returns a copy of env with the metrics endpoint pinned to
// endpoint and the flusher marker set. Any inherited BEADS_METRICS_ENDPOINT (for
// example one a project .beads/.env loaded into the parent process) is dropped
// so the detached flusher cannot be redirected by project-controlled
// environment.
func flusherChildEnv(env []string, endpoint string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvEndpoint+"=") || strings.HasPrefix(kv, EnvIsFlusher+"=") {
			continue
		}
		out = append(out, kv)
	}
	if endpoint != "" {
		out = append(out, EnvEndpoint+"="+endpoint)
	}
	out = append(out, EnvIsFlusher+"=1")
	return out
}

func flushDisabledByEnv() bool {
	v := os.Getenv(EnvDisableEventFlush)
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false":
		return false
	}
	return true
}
