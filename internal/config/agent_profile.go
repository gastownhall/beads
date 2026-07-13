package config

import "strings"

// AgentProfile represents the explicit policy profile that controls agent
// git/commit authority (gh#3423, follow-up to #4220's PROFILE_VOCABULARY doc).
//
// See docs/getting-started/ide-setup.md "Policy Profiles" for the full description of each value.
type AgentProfile string

const (
	// ProfileConservative is the default: report changed files, validation,
	// and proposed commands; do not commit, push, or run Dolt remote sync
	// without explicit user or orchestrator approval.
	ProfileConservative AgentProfile = "conservative"
	// ProfileMinimal has the same git authority as ProfileConservative; it
	// only differs in how much text hook-first integrations install.
	ProfileMinimal AgentProfile = "minimal"
	// ProfileTeamMaintainer allows an agent to close beads, run quality
	// gates, commit, `bd dolt push`, and `git push` as part of routine work,
	// subordinate to any explicit "do not commit"/"do not push" instruction.
	ProfileTeamMaintainer AgentProfile = "team-maintainer"
)

// validAgentProfiles is the set of allowed agent.profile values.
var validAgentProfiles = map[AgentProfile]bool{
	ProfileConservative:   true,
	ProfileMinimal:        true,
	ProfileTeamMaintainer: true,
}

// ValidAgentProfiles returns the list of valid agent.profile values.
func ValidAgentProfiles() []string {
	return []string{
		string(ProfileConservative),
		string(ProfileMinimal),
		string(ProfileTeamMaintainer),
	}
}

// IsValidAgentProfile returns true if the given string is a recognized
// agent profile (case-insensitive).
func IsValidAgentProfile(profile string) bool {
	return validAgentProfiles[AgentProfile(strings.ToLower(strings.TrimSpace(profile)))]
}

// GetAgentProfile retrieves the explicit agent policy profile.
//
// Config key: agent.profile
// Env var: BD_AGENT_PROFILE (bound automatically via viper's BD env prefix)
// Valid values: conservative | minimal | team-maintainer
//
// Returns ProfileConservative if unset, empty, or invalid; an invalid
// non-empty value logs a warning rather than erroring, matching the
// fallback behavior of GetSovereignty.
func GetAgentProfile() AgentProfile {
	value := strings.ToLower(strings.TrimSpace(GetString("agent.profile")))
	if value == "" {
		return ProfileConservative
	}

	profile := AgentProfile(value)
	if !validAgentProfiles[profile] {
		logConfigWarning("Warning: invalid agent.profile %q in config (valid: %s), using %q\n",
			value, strings.Join(ValidAgentProfiles(), ", "), ProfileConservative)
		return ProfileConservative
	}

	return profile
}

// String returns the string representation of the AgentProfile.
func (p AgentProfile) String() string {
	return string(p)
}
