package main

// HistoryCapabilityClass classifies history/version-control commands on the
// proxied topology without coupling them to a particular backend engine.
type HistoryCapabilityClass string

const (
	HistoryProxySupported HistoryCapabilityClass = "proxy-supported"
	HistoryProxyAPI       HistoryCapabilityClass = "proxy-api"
	HistoryDirectOnly     HistoryCapabilityClass = "direct-only"
)

var historyCapabilityMatrix = map[string]HistoryCapabilityClass{
	"branch": HistoryDirectOnly, "conflicts": HistoryDirectOnly, "repo": HistoryDirectOnly,
	"federation": HistoryDirectOnly, "vc": HistoryDirectOnly, "flatten": HistoryDirectOnly,
	"dolt push": HistoryDirectOnly, "dolt pull": HistoryDirectOnly, "dolt commit": HistoryDirectOnly,
	"dolt remote": HistoryDirectOnly, "sync": HistoryDirectOnly,
}

// LookupHistoryCapability classifies an exact command path. Unknown paths are
// intentionally absent so callers cannot accidentally advertise support.
func LookupHistoryCapability(commandPath string) (HistoryCapabilityClass, bool) {
	c, ok := historyCapabilityMatrix[commandPath]
	return c, ok
}
