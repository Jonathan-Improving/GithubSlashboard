package provider

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Kind is the closed set of provider invocation strategies, mirroring the
// configuration enum. It is duplicated here (rather than importing config) so
// the provider package stays a leaf the core wires into, not coupled to the
// config struct (POLICY: the provider is pluggable and never core).
type Kind string

const (
	// KindOneShot spawns a fresh subprocess per request (Ollama-style).
	KindOneShot Kind = "oneshot"
	// KindSession reuses one long-lived harness session (Kiro/Claude/Grok).
	KindSession Kind = "session"
)

// Options fully describes how to construct a provider. The core populates it
// from configuration and hands it to NewFromOptions, the single seam through
// which a provider is obtained.
type Options struct {
	// Name identifies the provider/harness (e.g. "kiro").
	Name string
	// Kind selects the invocation strategy.
	Kind Kind
	// Command, when set, overrides the harness argv (both kinds). For a session
	// provider it is the interactive launch argv; for one-shot it is the argv
	// whose stdin receives the prompt.
	Command []string

	// Session-only settings.
	TmuxBin      string        // tmux executable (default "tmux")
	ClearCommand string        // harness context-reset command (e.g. "/clear")
	ReadyTimeout time.Duration // max wait for the harness to become interactive
	Settle       time.Duration // quiet window confirming the harness is idle
	VerdictTool  string        // MCP tool name the harness calls to return a verdict
	IdleMarker   string        // pane substring shown when the harness is idle
	BusyMarker   string        // pane substring shown when the harness is mid-turn

	// PoolSize, when > 1, runs that many session harnesses in parallel (session
	// kind only), dispatching concurrent classifications across them. Defaults
	// to 1 (a single session) when unset.
	PoolSize int
}

// NewFromOptions builds a provider from Options. One-shot delegates to the
// existing subprocess provider; session builds the tmux + MCP-sink provider,
// provisioning a harness profile that enables the verdict tool (TDD 6.7, 6.8).
func NewFromOptions(o Options) (Provider, error) {
	switch o.Kind {
	case KindOneShot, "":
		return New(o.Name, o.Command)
	case KindSession:
		if o.PoolSize > 1 {
			// Each pool member is an independent harness session; build with the
			// same options but neutralize PoolSize to avoid recursion.
			member := o
			member.PoolSize = 1
			return newPoolProvider(o.Name, o.PoolSize, func() (Provider, error) {
				return newHarnessSession(member)
			})
		}
		return newHarnessSession(o)
	default:
		return nil, fmt.Errorf("unknown provider kind %q", o.Kind)
	}
}

// verdictToolDefault is the MVP name of the tool the harness calls to return
// its verdict. A closed protocol identifier, so a named constant.
const verdictToolDefault = "submit_verdict"

// newHarnessSession assembles a session provider: it starts the MCP sink, then
// provisions a harness profile that points at the sink and instructs the model
// to return its verdict through the verdict tool, then launches the harness
// interactively under tmux.
func newHarnessSession(o Options) (Provider, error) {
	toolName := o.VerdictTool
	if toolName == "" {
		toolName = verdictToolDefault
	}
	sink, err := newMCPSink(toolName)
	if err != nil {
		return nil, err
	}

	argv := o.Command
	clearCmd := o.ClearCommand
	idleMarker := o.IdleMarker
	busyMarker := o.BusyMarker
	workDir := ""
	var cleanup func()
	if len(argv) == 0 {
		// MVP harness: Kiro CLI, launched interactively (not --no-interactive)
		// under a provisioned named profile that enables only the verdict tool.
		// kiro-cli resolves --agent by NAME from a .kiro/agents directory in the
		// working dir (or globally), not by file path, so the profile is written
		// as <name>.json into a temp workspace the session runs in.
		agentName := "githubslashboard-session"
		dir, perr := provisionKiroProfile(sink.Endpoint(), toolName, agentName)
		if perr != nil {
			_ = sink.Close()
			return nil, perr
		}
		workDir = dir
		cleanup = func() { _ = os.RemoveAll(dir) }
		argv = kiroSessionArgv(agentName)
		if clearCmd == "" {
			clearCmd = kiroClearCommand
		}
		if idleMarker == "" {
			idleMarker = kiroIdleMarker
		}
		if busyMarker == "" {
			busyMarker = kiroBusyMarker
		}
	}

	tmuxBin := o.TmuxBin
	if tmuxBin == "" {
		tmuxBin = "tmux"
	}
	if _, lerr := lookupTmux(tmuxBin); lerr != nil {
		if cleanup != nil {
			cleanup()
		}
		_ = sink.Close()
		return nil, fmt.Errorf("session provider requires tmux: %w", lerr)
	}
	ready := o.ReadyTimeout
	if ready <= 0 {
		ready = 90 * time.Second
	}
	session := uniqueTmuxSessionName(tmuxBin)
	transport := newTmuxTransport(tmuxBin, session, argv, ready)
	transport.workDir = workDir
	transport.cleanup = cleanup
	transport.idleMarker = idleMarker
	transport.busyMarker = busyMarker
	if o.Settle > 0 {
		transport.settle = o.Settle
	}

	sp := newSessionProvider(o.Name, transport, sink, clearCmd, o.Settle, ready)
	// Prompt files are handed to the harness through a directory its read tool
	// is scoped to. When we provisioned the workspace, that is <workspace>/prompts;
	// otherwise fall back to the OS temp dir.
	if workDir != "" {
		sp.promptDir = filepath.Join(workDir, "prompts")
	}
	return sp, nil
}

// Kiro CLI interactive-harness constants. These identify the idle vs. busy
// states in kiro-cli's terminal UI and its context-reset command. They are the
// MVP harness's protocol markers; a different harness supplies its own via
// Options.
const (
	kiroClearCommand = "/clear"
	kiroIdleMarker   = "ask a question or describe a task"
	kiroBusyMarker   = "Kiro is working"
)

// kiroSessionArgv is the MVP Kiro CLI interactive launch for a reused session.
// It runs interactively (no --no-interactive) under the provisioned profile,
// referenced by name. It does NOT pass --trust-all-tools: that flag forces an
// interactive consent menu that blocks startup; instead the profile's
// allowedTools pre-approves the single verdict tool so no prompt appears.
func kiroSessionArgv(agentName string) []string {
	return []string{
		"kiro-cli", "chat",
		"--agent", agentName,
	}
}

// provisionKiroProfile writes a Kiro agent profile named agentName that enables
// only the verdict MCP tool, points it at the sink URL over streamable HTTP,
// and instructs the model to classify a priori and return its verdict by
// calling the tool. The profile is deliberately narrow (POLICY ratifies exactly
// this one tool): a bare classification context plus the single sink tool. It
// is written as <workspace>/.kiro/agents/<agentName>.json and the workspace dir
// is returned so the session can run with it as the working directory (kiro-cli
// resolves --agent by name from that location).
func provisionKiroProfile(sinkURL, toolName, agentName string) (string, error) {
	workspace, err := os.MkdirTemp("", "gsb-kiro-session-")
	if err != nil {
		return "", fmt.Errorf("temp workspace: %w", err)
	}
	promptsDir := filepath.Join(workspace, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		_ = os.RemoveAll(workspace)
		return "", fmt.Errorf("make prompts dir: %w", err)
	}

	profile := map[string]any{
		"$schema":     "https://raw.githubusercontent.com/aws/amazon-q-developer-cli/refs/heads/main/schemas/agent-v1.json",
		"name":        agentName,
		"description": "Bare a-priori PR classifier that reads its input from a file and returns its verdict through the verdict MCP tool.",
		"model":       "glm-5",
		// Do not merge the global legacy mcp.json: this session must have a bare
		// context with only the verdict tool, no unrelated MCP servers, so the
		// harness starts fast and stays focused. This is the in-profile control
		// that the lean agent alone could not achieve.
		"useLegacyMcpJson": false,
		"mcpServers": map[string]any{
			"verdict": map[string]any{
				"url":      sinkURL,
				"disabled": false,
			},
		},
		// Two deliberately-introduced, tightly-scoped capabilities (POLICY):
		//   - the verdict MCP tool (how the classification is returned), and
		//   - fs_read, scoped to the prompts directory only, so the harness can
		//     read the per-PR input file (the event trail is too large to type
		//     into the session without hitting the terminal command-length
		//     limit). No write, shell, or network tools reach this session.
		// Listing them in allowedTools pre-approves them so no interactive trust
		// prompt appears at startup.
		"tools":        []string{"@verdict/" + toolName, "fs_read"},
		"allowedTools": []string{"@verdict/" + toolName, "fs_read"},
		"toolsSettings": map[string]any{
			"fs_read": map[string]any{
				"allowedPaths": []string{promptsDir},
			},
		},
		"prompt": "You are a classification function for a read-only GitHub dashboard. " +
			"Each request gives you a file path. Read that file: it contains one item's " +
			"identifying context and chronological event trail as JSON, plus response instructions. " +
			"The item is either a pull request or an issue — its \"entity\" field says which, and its " +
			"\"constraints\" field lists the exact vocabulary that item's verdict may use. " +
			"Classify the item's true status a priori from that trail alone, honoring the immutable floors " +
			"(merged stays merged, closed stays closed) and letting later authoritative events supersede " +
			"earlier uncleared flags. Return your verdict by calling the " + toolName + " tool exactly once " +
			"with fields bucket, action (only if bucket is open), close_reason (only if bucket is closed), " +
			"priority, companion, and emoji (exactly one emoji character summarizing the note). " +
			"Every enum value must come from the matching list in the request's constraints. " +
			"Do not print the verdict as text; only call the tool.",
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		_ = os.RemoveAll(workspace)
		return "", fmt.Errorf("marshal kiro profile: %w", err)
	}
	agentsDir := filepath.Join(workspace, ".kiro", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		_ = os.RemoveAll(workspace)
		return "", fmt.Errorf("make agents dir: %w", err)
	}
	path := filepath.Join(agentsDir, agentName+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(workspace)
		return "", fmt.Errorf("write kiro profile: %w", err)
	}
	return workspace, nil
}

// tmuxSessionPrefix is the human-friendly prefix for a harness's tmux session
// name; a short random suffix disambiguates concurrent/successive sessions.
const tmuxSessionPrefix = "GSB-Harvester-"

// sessionSuffixLen is the number of random alphanumeric characters appended to
// tmuxSessionPrefix to form a unique session name.
const sessionSuffixLen = 6

// sessionSuffixAlphabet is the character set the random suffix is drawn from.
const sessionSuffixAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// uniqueTmuxSessionName returns a session name of the form
// "GSB-Harvester-<6 random alphanumerics>" that no live tmux session already
// uses. It regenerates on collision (checked via `tmux has-session`), bounded
// so a persistently failing tmux can't spin forever — after the bound it
// returns the last candidate and lets new-session surface any real error.
func uniqueTmuxSessionName(tmuxBin string) string {
	const maxTries = 1000
	var name string
	for i := 0; i < maxTries; i++ {
		name = tmuxSessionPrefix + randomSuffix(sessionSuffixLen)
		if !tmuxSessionExists(tmuxBin, name) {
			return name
		}
	}
	return name
}

// tmuxSessionExists reports whether a tmux session with the given name is
// currently live. `tmux has-session` exits 0 when it exists, non-zero
// otherwise (including when no server is running), so a non-nil error is read
// as "does not exist".
func tmuxSessionExists(tmuxBin, name string) bool {
	if tmuxBin == "" {
		tmuxBin = "tmux"
	}
	return exec.Command(tmuxBin, "has-session", "-t", name).Run() == nil
}

// randomSuffix returns n characters drawn uniformly from sessionSuffixAlphabet
// using a cryptographic source. It panics only if the system RNG fails, which
// is not a recoverable condition.
func randomSuffix(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("provider: read random for session name: %v", err))
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = sessionSuffixAlphabet[int(b)%len(sessionSuffixAlphabet)]
	}
	return string(out)
}

// lookupTmux resolves the tmux executable path, for early validation of a
// session provider's environment.
func lookupTmux(bin string) (string, error) {
	if bin == "" {
		bin = "tmux"
	}
	return exec.LookPath(bin)
}
