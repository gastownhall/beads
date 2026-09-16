package main

// `bd dolt start` and `bd dolt status` for proxied-server workspaces.
//
// Both commands predate proxied mode and assume bd owns the sql-server: they
// read and write .beads/dolt-server.pid. A proxied workspace never has one —
// the proxy writes proxy.pid and proxy-child.pid under the proxied root — so
// status used to report "not running" against a healthy backend, and start
// used to act on that by putting a second sql-server over the live data
// directory. This file is the proxied-aware half of both.

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// Stable identity of the `bd dolt start` refusal on a proxied workspace.
// Codes are the part of the refusal contract downstream consumers branch on,
// so this one is frozen; the message beside it is informative.
const proxyDoltStartConflictCode = "proxy.dolt_start.conflict"

const proxyDoltStartConflictMessage = "dolt start is not supported in proxied-server mode: " +
	"the proxy owns its dolt backend, and a second sql-server over the same data directory risks corrupting it; " +
	"the proxy starts on demand — run 'bd dolt status' to see what is running, or 'bd dolt stop' to shut it down"

// proxiedDoltStartRefusal is enforced inside the command rather than by the
// pre-provider capability gate because `bd dolt` is a noDbCommands entry: its
// subcommands skip store init and so never reach the gate in main.go. Folding
// this row into the consolidated registry is S2's job; the corruption guard
// should not wait for it.
func proxiedDoltStartRefusal() *ProxyCapabilityError {
	return &ProxyCapabilityError{
		Code:     proxyDoltStartConflictCode,
		Message:  proxyDoltStartConflictMessage,
		ExitCode: 1,
	}
}

// proxiedDoltStatus is the `bd dolt status --json` payload for a proxied
// workspace. It reports the two processes separately because they fail
// separately and an operator's next move differs: Running describes the proxy,
// which is the endpoint every bd command connects through, while the Backend*
// fields describe the dolt server behind it.
type proxiedDoltStatus struct {
	Mode      string `json:"mode"`
	Root      string `json:"root"`
	Running   bool   `json:"running"`
	ProxyPID  int    `json:"proxy_pid,omitempty"`
	ProxyPort int    `json:"proxy_port,omitempty"`
	// BackendManaged is false on an external proxied topology, where the
	// dolt server is somebody else's process and BackendRunning says nothing
	// about it. Without this flag a caller cannot tell "the backend is down"
	// from "bd never had a backend to report".
	BackendManaged  bool   `json:"backend_managed"`
	BackendRunning  bool   `json:"backend_running"`
	BackendPID      int    `json:"backend_pid,omitempty"`
	BackendPort     int    `json:"backend_port,omitempty"`
	BackendEndpoint string `json:"backend_endpoint,omitempty"`
	IdleTimeout     string `json:"idle_timeout,omitempty"`
}

func collectProxiedDoltStatus(beadsDir string) (proxiedDoltStatus, error) {
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return proxiedDoltStatus{}, err
	}
	live := proxy.ReadStatus(root)
	status := proxiedDoltStatus{
		Mode:           configfile.DoltModeProxiedServer,
		Root:           root,
		Running:        live.ProxyRunning,
		ProxyPID:       live.ProxyPID,
		ProxyPort:      live.ProxyPort,
		BackendManaged: true,
		BackendRunning: live.BackendRunning,
		BackendPID:     live.BackendPID,
		BackendPort:    live.BackendPort,
	}

	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return proxiedDoltStatus{}, err
	}
	// A missing sidecar means the workspace predates it or was hand-edited;
	// managed-local is the default topology, and the pid records already read
	// above are the authority on what is actually running either way.
	if info != nil {
		if info.IdleTimeout > 0 {
			status.IdleTimeout = info.IdleTimeout.String()
		}
		if info.External != nil {
			status.BackendManaged = false
			status.BackendEndpoint = externalDoltEndpoint(*info.External)
		}
	}
	return status, nil
}

func externalDoltEndpoint(cfg configfile.ExternalDoltConfig) string {
	if cfg.Socket != "" {
		return "unix:" + cfg.Socket
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

func renderProxiedDoltStatus(status proxiedDoltStatus) {
	if jsonOutput {
		if err := outputJSON(status); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return
	}

	if status.Running {
		fmt.Printf("Dolt server: running (%s)\n", status.Mode)
		fmt.Printf("  Proxy PID:  %d\n", status.ProxyPID)
		fmt.Printf("  Proxy port: %d\n", status.ProxyPort)
	} else {
		fmt.Printf("Dolt server: not running (%s)\n", status.Mode)
	}
	fmt.Printf("  Root:       %s\n", status.Root)
	switch {
	case !status.BackendManaged:
		fmt.Printf("  Backend:    external at %s (not managed by bd)\n", status.BackendEndpoint)
	case status.BackendRunning:
		fmt.Printf("  Backend:    running (dolt PID %d, port %d)\n", status.BackendPID, status.BackendPort)
	default:
		fmt.Println("  Backend:    not running")
	}
	if status.IdleTimeout != "" {
		fmt.Printf("  Idle timeout: %s\n", status.IdleTimeout)
	}
	if !status.Running {
		fmt.Println("  The proxy starts on demand; the next bd command launches it.")
	}
	if isDoltLocalOnly() {
		fmt.Println("  Remote sync: disabled (dolt.local-only=true)")
	}
}
