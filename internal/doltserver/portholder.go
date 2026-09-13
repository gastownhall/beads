package doltserver

// PortHolderOutcome is the three-valued result of a port-holder lookup. It is
// three-valued on purpose: the older findPIDOnPort returns 0 for "nothing is
// listening" AND for every failure it can hit — lsof or netstat missing, exec
// denied, output in an unexpected shape — so a caller reading it as a boolean
// treats "I could not look" as "nothing is there". That fails open, and the
// gates that use this must fail closed.
type PortHolderOutcome int

const (
	// PortHolderUndetermined means the lookup could not be performed or its
	// result could not be trusted. It is never evidence that a port is free.
	PortHolderUndetermined PortHolderOutcome = iota
	// PortHolderNoHolder means the lookup ran and found nothing listening.
	PortHolderNoHolder
	// PortHolderHeld means the lookup found a listener (and, when a directory
	// was supplied, bound it to that directory).
	PortHolderHeld
)

func (o PortHolderOutcome) String() string {
	switch o {
	case PortHolderHeld:
		return "held"
	case PortHolderNoHolder:
		return "no_holder"
	default:
		return "undetermined"
	}
}

// ResolvePortHolderInDir answers "which process is listening on this loopback
// port, and is it this workspace's server?" — the observation an ownership
// transfer needs before it will believe anything about a server it did not
// launch.
//
// dir narrows the answer. With dir set, PortHolderHeld means the port holder is
// also bound to that directory, and boundBy names the evidence: "fd-lock" when
// the process holds a descriptor under dir (the strongest binding — a Dolt
// server keeps its noms LOCK open for as long as it serves), or "cwd" when only
// the working directory matches. A holder that could not be bound to dir is
// PortHolderNoHolder for this question: something is listening, but not this
// workspace's server. With dir empty the binding step is skipped, boundBy is "",
// and the question is the weaker "is this endpoint free?".
//
// PortHolderUndetermined is the answer whenever the lookup itself failed.
// Callers must record it as unavailable evidence and must not read it as either
// a holder or a free port.
//
// PortHolderSource names the mechanism, which differs per platform and belongs
// in the journal next to any gate that rested on it.
func ResolvePortHolderInDir(port int, dir string) (pid int, boundBy string, outcome PortHolderOutcome) {
	if port <= 0 {
		return 0, "", PortHolderUndetermined
	}
	return resolvePortHolderInDir(port, dir)
}

// PortHolderSource names the mechanism ResolvePortHolderInDir uses here:
// "proc" (Linux procfs), "lsof" (darwin), "netstat" (Windows), or "" where no
// lookup exists. A gate whose source is "" is recorded unavailable.
func PortHolderSource() string { return portHolderSource }
