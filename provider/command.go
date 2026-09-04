package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// CommandProvider invokes an LLM through a configured command as a bounded
// subprocess (TDD 6.1, 6.2). It writes a prompt to the subprocess's stdin and
// reads its stdout. It is the single implementation behind both the MVP Kiro
// CLI and any Grok/Claude/Ollama-style command provider — they differ only by
// the argv passed in, so no core change is needed to add one (TDD 6.2).
type CommandProvider struct {
	name string
	argv []string
}

// NewCommandProvider builds a CommandProvider from an explicit argv. argv[0] is
// the executable; the remaining elements are fixed leading arguments. The
// composed prompt is delivered on stdin.
func NewCommandProvider(name string, argv []string) (*CommandProvider, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("provider command is empty")
	}
	return &CommandProvider{name: name, argv: argv}, nil
}

// kiroOneShotArgv is the Kiro CLI one-shot invocation: a non-interactive,
// headless chat that reads the prompt from stdin and prints the model's reply
// to stdout. It pins a lean agent profile (no tools, no MCP servers) and the
// given model so classification runs are cheap and free of unrelated tool
// context. The prompt (the PR event trail) is delivered on stdin as the
// positional input.
func kiroOneShotArgv(model string) []string {
	return []string{
		"kiro-cli", "chat", "--no-interactive",
		"--agent", "githubslashboard",
		"--model", model,
		"--trust-tools=",
	}
}

// ollamaArgv is the one-shot Ollama invocation: `ollama run <model>`, prompt
// delivered on stdin. This is the fixed, blackboxed shape for the oneshot kind
// (POLICY: no raw-command escape hatch) — the only thing that varies between
// deployments or between the primary/fallback slot is which model is named.
func ollamaArgv(model string) []string {
	return []string{"ollama", "run", model}
}

// newOneShotProvider builds a CommandProvider for a one-shot-kind name (TDD
// 6.11), using that name's fixed, blackboxed argv shape parameterized only by
// model. name must already be known-oneshot (checked by the caller via
// KindForName) — this function itself only knows how to build the argv for
// each currently-supported one-shot name.
func newOneShotProvider(name, model string) (*CommandProvider, error) {
	switch name {
	case "kiro":
		if model == "" {
			model = defaultKiroModel
		}
		return NewCommandProvider(name, kiroOneShotArgv(model))
	case "ollama":
		if model == "" {
			return nil, fmt.Errorf("provider %q requires a model", name)
		}
		return NewCommandProvider(name, ollamaArgv(model))
	default:
		return nil, fmt.Errorf("no one-shot construction known for provider %q", name)
	}
}

// Name identifies the provider for logging.
func (c *CommandProvider) Name() string { return c.name }

// Invoke runs the subprocess for one request, bounded by ctx (TDD 6.5). The
// request is rendered to a prompt on stdin; the raw stdout is returned for the
// caller to parse and validate.
func (c *CommandProvider) Invoke(ctx context.Context, req Request, correction string) (string, error) {
	prompt, err := buildPrompt(req, correction)
	if err != nil {
		return "", err
	}
	return c.run(ctx, prompt)
}

// Summarize runs the subprocess for one plain-text prompt, bounded by ctx
// (TDD 6.9). Unlike Invoke there is no Request to render and no vocabulary to
// validate the response against — the prompt is used as-is and the raw stdout
// is returned verbatim, since a summary sentence has no structure to parse.
func (c *CommandProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	return c.run(ctx, prompt)
}

// run is the shared subprocess mechanics behind Invoke and Summarize: write
// prompt to the configured command's stdin, bounded by ctx, and return its
// stdout. Both call shapes reduce to the same "one prompt in, one response
// out" subprocess pattern for a one-shot provider (TDD 6.2) — what differs
// between them is entirely in how the caller built the prompt and what it does
// with the response, not in how the process itself is run.
func (c *CommandProvider) run(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("provider %s timed out", c.name)
		}
		return "", fmt.Errorf("provider %s failed: %w (stderr: %s)", c.name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// buildPrompt renders the request into the textual prompt sent to the provider.
// The event trail is delivered as JSON so the provider receives a strict,
// parse-friendly structure (TDD 6.3); the instructions restate the required
// response shape and point at the vocabularies carried in the request, which are
// entity-specific (a PR and an issue do not share an action set).
func buildPrompt(req Request, correction string) (string, error) {
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal provider request: %w", err)
	}

	var b strings.Builder
	if req.Entity == EntityIssue {
		b.WriteString("You are classifying the true status of one GitHub issue from its ")
		b.WriteString("chronological event trail. A later authoritative event supersedes an earlier ")
		b.WriteString("uncleared signal. Judge whether the issue awaits a response from the operator, ")
		b.WriteString("awaits someone else, still needs triage, or is resolved pending close.\n\n")
	} else {
		b.WriteString("You are classifying the true status of one GitHub pull request from its ")
		b.WriteString("chronological event trail. A later authoritative event supersedes an earlier ")
		b.WriteString("uncleared flag. Honor the immutable floors: if github_state is \"merged\" the ")
		b.WriteString("bucket must be \"merged\"; if \"closed\" the bucket must be \"closed\".\n\n")
	}
	b.WriteString("Input (JSON):\n")
	b.Write(payload)
	b.WriteString("\n\n")
	b.WriteString("Respond with exactly one fenced ```json object with fields: bucket, ")
	b.WriteString("action (only if bucket==open), close_reason (only if bucket==closed), ")
	b.WriteString("priority, companion, emoji. Every enum value must be drawn from the matching ")
	b.WriteString("list in the input's constraints. The companion is a short note within the ")
	b.WriteString("configured word bounds. The emoji is exactly one emoji character that visually ")
	b.WriteString("summarizes the note. Output only the JSON object.\n")

	if correction != "" {
		b.WriteString("\n")
		b.WriteString(correction)
		b.WriteString("\n")
	}
	return b.String(), nil
}
