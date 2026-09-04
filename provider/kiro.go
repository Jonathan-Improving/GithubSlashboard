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

// kindByName is the single closed mapping from a provider name to its
// invocation kind (TDD 6.11). It is the only place in the codebase that
// associates a name with a kind, so pairing a name with any other kind is
// unrepresentable rather than merely rejected by validation — there is no
// configuration value that could name a kind independently, for either the
// primary or fallback slot (TDD 6.12).
var kindByName = map[string]Kind{
	"kiro":   KindSession,
	"ollama": KindOneShot,
}

// KindForName returns the fixed invocation kind for a provider name, or an
// error if the name is not a member of the closed set this build supports.
func KindForName(name string) (Kind, error) {
	k, ok := kindByName[name]
	if !ok {
		return "", fmt.Errorf("unknown provider %q", name)
	}
	return k, nil
}

// Options fully describes how to construct a provider. The core populates it
// from configuration and hands it to NewFromOptions, the single seam through
// which a provider is obtained. There is deliberately no Kind or Command
// field: the invocation kind is derived solely from Name (KindForName,
// TDD 6.11), and the harness/subprocess argv is a fixed, blackboxed shape per
// kind, parameterized only by Model — never an operator-supplied command line
// (POLICY: no raw-command escape hatch for either provider slot).
type Options struct {
	// Name identifies the provider (e.g. "kiro", "ollama"). Its invocation kind
	// is derived from this name alone (KindForName) — never independently
	// configured, so a name can never be paired with the wrong kind (TDD 6.11).
	Name string
	// Model selects the model within Name's fixed kind (e.g. "glm-5" for kiro,
	// "glm-4.7-flash" for ollama). Both the primary and fallback slot use this
	// same field — the two slots are symmetric, differing only in when each is
	// invoked (TDD 6.12).
	Model string

	// Session-only settings.
	TmuxBin      string        // tmux executable (default "tmux")
	ClearCommand string        // harness context-reset command (e.g. "/clear")
	ReadyTimeout time.Duration // max wait for the harness to become interactive
	Settle       time.Duration // quiet window confirming the harness is idle
	VerdictTool  string        // MCP tool name the harness calls to return a verdict
	SummaryTool  string        // MCP tool name the harness calls to return a summary (TDD 6.9)
	IdleMarker   string        // pane substring shown when the harness is idle
	BusyMarker   string        // pane substring shown when the harness is mid-turn

	// PoolSize, when > 1, runs that many session harnesses in parallel (session
	// kind only), dispatching concurrent classifications across them. Defaults
	// to 1 (a single session) when unset.
	PoolSize int
}

// NewFromOptions builds a provider from Options. The invocation kind is
// derived from o.Name (KindForName, TDD 6.11) — one-shot delegates to the
// blackboxed subprocess provider parameterized by Model; session builds the
// tmux + MCP-sink provider, provisioning a harness profile that enables the
// verdict tool (TDD 6.7, 6.8).
func NewFromOptions(o Options) (Provider, error) {
	kind, err := KindForName(o.Name)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindOneShot:
		return newOneShotProvider(o.Name, o.Model)
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
		return nil, fmt.Errorf("unknown provider kind %q", kind)
	}
}

// verdictToolDefault is the MVP name of the tool the harness calls to return
// its classification verdict. summaryToolDefault is the MVP name of the tool
// it calls to return a notification summary sentence (TDD 6.9). Both are
// closed protocol identifiers, so named constants; the two are mutually
// exclusive per turn (TDD 6.10), never both offered at once.
const (
	verdictToolDefault = "submit_verdict"
	summaryToolDefault = "submit_summary"
)

// newHarnessSession assembles a session provider: it starts the MCP sink, then
// provisions a harness profile that points at the sink and instructs the model
// to return its verdict through the verdict tool, then launches the harness
// interactively under tmux. The harness argv is always the fixed, blackboxed
// Kiro shape (POLICY: no raw-command escape hatch) — only o.Model varies it.
func newHarnessSession(o Options) (Provider, error) {
	toolName := o.VerdictTool
	if toolName == "" {
		toolName = verdictToolDefault
	}
	summaryToolName := o.SummaryTool
	if summaryToolName == "" {
		summaryToolName = summaryToolDefault
	}
	sink, err := newMCPSink(toolName, summaryToolName)
	if err != nil {
		return nil, err
	}

	model := o.Model
	if model == "" {
		model = defaultKiroModel
	}
	clearCmd := o.ClearCommand
	idleMarker := o.IdleMarker
	busyMarker := o.BusyMarker

	// The harness is always the Kiro CLI, launched interactively (not
	// --no-interactive) under a provisioned named profile that enables only the
	// verdict and summary tools. kiro-cli resolves --agent by NAME from a
	// .kiro/agents directory in the working dir (or globally), not by file
	// path, so the profile is written as <name>.json into a temp workspace the
	// session runs in.
	agentName := "githubslashboard-session"
	workDir, perr := provisionKiroProfile(sink.Endpoint(), toolName, summaryToolName, agentName, model)
	if perr != nil {
		_ = sink.Close()
		return nil, perr
	}
	cleanup := func() { _ = os.RemoveAll(workDir) }
	argv := kiroSessionArgv(agentName)
	if clearCmd == "" {
		clearCmd = kiroClearCommand
	}
	if idleMarker == "" {
		idleMarker = kiroIdleMarker
	}
	if busyMarker == "" {
		busyMarker = kiroBusyMarker
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

	sp := newSessionProvider(o.Name, transport, sink, clearCmd, o.Settle, ready, toolName, summaryToolName)
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

	// defaultKiroModel is used when Options.Model is empty, preserving today's
	// behavior for a deployment that does not set GSB_PROVIDER_MODEL /
	// GSB_FALLBACK_PROVIDER_MODEL.
	defaultKiroModel = "glm-5"
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

// provisionKiroProfile writes a Kiro agent profile named agentName that trusts
// both the verdict and summary MCP tools (TDD 6.10), points it at the sink URL
// over streamable HTTP, and instructs the model to act only on whichever tool
// the current turn actually offers. The profile is deliberately narrow (POLICY
// ratifies exactly these two tools): a bare context plus the sink's two tools.
// Trusting both here is a one-time, static pre-approval — the sink itself
// advertises only one per turn (mcpSink.SetActiveTool), so the harness is never
// actually offered a choice even though the profile trusts both names. It is
// written as <workspace>/.kiro/agents/<agentName>.json and the workspace dir is
// returned so the session can run with it as the working directory (kiro-cli
// resolves --agent by name from that location). model selects the model the
// profile pins (TDD 6.12: symmetric with the one-shot provider's own Model
// parameterization).
func provisionKiroProfile(sinkURL, verdictTool, summaryTool, agentName, model string) (string, error) {
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
		"description": "Bare a-priori PR/issue classifier and notification summarizer that reads its input from a file and returns its result through whichever result MCP tool the current turn offers.",
		"model":       model,
		// Do not merge the global legacy mcp.json: this session must have a bare
		// context with only the result tools, no unrelated MCP servers, so the
		// harness starts fast and stays focused. This is the in-profile control
		// that the lean agent alone could not achieve.
		"useLegacyMcpJson": false,
		"mcpServers": map[string]any{
			"result": map[string]any{
				"url":      sinkURL,
				"disabled": false,
			},
		},
		// Three deliberately-introduced, tightly-scoped capabilities (POLICY):
		//   - the verdict tool (classification result) and the summary tool
		//     (notification result) — both trusted here as a static
		//     pre-approval, but the sink advertises only one per turn
		//     (TDD 6.10), so the harness is never actually offered a choice
		//     between them despite both being nominally trusted; and
		//   - fs_read, scoped to the prompts directory only, so the harness can
		//     read the per-request input file (an event trail or a summary
		//     prompt is too large to type into the session without hitting the
		//     terminal command-length limit). No write, shell, or network tools
		//     reach this session.
		// Listing them in allowedTools pre-approves them so no interactive
		// trust prompt appears at startup or when the active tool switches.
		"tools":        []string{"@result/" + verdictTool, "@result/" + summaryTool, "fs_read"},
		"allowedTools": []string{"@result/" + verdictTool, "@result/" + summaryTool, "fs_read"},
		"toolsSettings": map[string]any{
			"fs_read": map[string]any{
				"allowedPaths": []string{promptsDir},
			},
		},
		"prompt": "You perform two kinds of turn for a read-only GitHub dashboard: classifying one pull " +
			"request or issue's true status, or writing a one-sentence notification summary. Each request " +
			"gives you a file path or inline text with its own instructions — follow those instructions for " +
			"what this specific turn wants. " +
			"For a classification turn: the file contains one item's identifying context and chronological " +
			"event trail as JSON. The item is either a pull request or an issue — its \"entity\" field says " +
			"which, and its \"constraints\" field lists the exact vocabulary the verdict may use. Classify the " +
			"item's true status a priori from that trail alone, honoring the immutable floors (merged stays " +
			"merged, closed stays closed) and letting later authoritative events supersede earlier uncleared " +
			"flags. Return your verdict by calling the " + verdictTool + " tool exactly once with fields " +
			"bucket, action (only if bucket is open), close_reason (only if bucket is closed), priority, " +
			"companion, and emoji (exactly one emoji character summarizing the note). Every enum value must " +
			"come from the matching list in the request's constraints. " +
			"For a summary turn: you are given a short description of what changed. Write exactly one short " +
			"sentence summarizing it, sized for a desktop notification, and return it by calling the " +
			summaryTool + " tool exactly once with field summary. " +
			"Only one of these two tools is ever offered to you in a given turn — call whichever one the " +
			"turn's own instructions ask for; do not guess or call a tool that was not asked for. Do not " +
			"print your result as text; only call the tool.",
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
