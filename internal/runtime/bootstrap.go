package runtime

import (
	"fmt"
	"os"
)

// BootstrapInput is the portable contract every runtime needs to provision
// agent content into the sandbox. Implementations live outside this package
// (runner adapter, tests).
type BootstrapInput interface {
	SandboxName() string
	// AgentPath returns the local filesystem path to the agent definition file.
	// For cached agents this may be a content-addressed path with a generic basename.
	AgentPath() string
	// AgentName returns the logical agent name (e.g. "review") used to construct
	// the destination filename as {name}.md inside the sandbox. Populated from
	// the CLI positional argument; must not be empty in production (enforced by
	// cobra arg validation in cmd/fullsend).
	AgentName() string
	SkillDirs() []string
	PluginDirs() []string
}

// validateAgentNameMatch returns an error when requestedName and
// definitionName are both non-empty and do not match. Both ClaudeRuntime
// and PiRuntime call this shared helper so the mismatch message is defined
// in one place.
func validateAgentNameMatch(requestedName, definitionName string) error {
	if requestedName == "" || definitionName == "" {
		return nil
	}
	if definitionName != requestedName {
		return fmt.Errorf("agent name mismatch: requested %q but definition declares %q", requestedName, definitionName)
	}
	return nil
}

// validateAgentName reads the agent definition at agentPath, extracts the
// frontmatter name: field, and returns an error when it does not match
// requestedName. Claude Code resolves --agent by the frontmatter name, not
// the filename, so a mismatch means the runtime will silently fall back to
// the default agent — producing an unconstrained run (#6764).
//
// When the definition has no frontmatter or no name: field, validation is
// skipped: the runtime uses its own resolution chain (filename, positional
// argument).
func validateAgentName(requestedName, agentPath string) error {
	if requestedName == "" {
		return nil
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		// Let the caller's own ReadFile produce the canonical error.
		return nil
	}
	def, err := parsePiAgent(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Agent name validation: skipped for %s: %v\n", agentPath, err)
		return nil
	}
	return validateAgentNameMatch(requestedName, def.Name)
}
