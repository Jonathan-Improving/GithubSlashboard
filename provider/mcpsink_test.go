package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// post sends a JSON-RPC body to the sink and returns the decoded envelope and
// the response headers.
func post(t *testing.T, url, sessionID, body string) (map[string]any, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	return env, resp.Header
}

func TestMCPSinkHandshakeAndVerdict(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()
	url := sink.Endpoint()

	// initialize: returns protocol version and a session id header.
	env, hdr := post(t, url, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if hdr.Get("Mcp-Session-Id") == "" {
		t.Error("initialize should set Mcp-Session-Id")
	}
	result, _ := env["result"].(map[string]any)
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	sessionID := hdr.Get("Mcp-Session-Id")

	// tools/list: advertises both tools (TDD 6.10).
	env, _ = post(t, url, sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ = env["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools/list returned %d tools, want 2", len(tools))
	}
	if got := toolNames(tools); got[0] != "submit_verdict" || got[1] != "submit_summary" {
		t.Errorf("tool names = %v", got)
	}

	// tools/call: the arguments become the raw verdict surfaced to Await.
	go func() {
		post(t, url, sessionID,
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}}}`)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	verdict, err := sink.Await(ctx)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	// The surfaced verdict must parse and validate through the shared parser.
	resp, perr := ParseResponse(verdict, ConstraintsFrom(3, 8))
	if perr != nil {
		t.Fatalf("verdict did not parse: %v (raw=%s)", perr, verdict)
	}
	if resp.Companion == "" {
		t.Error("verdict companion missing")
	}
}

func TestMCPSinkNotificationReturns202(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()

	req, _ := http.NewRequest(http.MethodPost, sink.Endpoint(),
		bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification status = %d, want 202", resp.StatusCode)
	}
}

func TestMCPSinkUnknownToolErrors(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()

	env, _ := post(t, sink.Endpoint(), "sess",
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"other_tool","arguments":{}}}`)
	if _, ok := env["error"]; !ok {
		t.Error("unknown tool should return a JSON-RPC error")
	}
}

// toolNames returns the names in a tools/list result, in order.
func toolNames(tools []any) []string {
	var out []string
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

// isErrorResult reports whether a tools/call response is a successful JSON-RPC
// response carrying a tool result flagged isError, which is how the sink refuses
// a call to the tool the current turn is not asking for (TDD 6.10).
func isErrorResult(env map[string]any) (string, bool) {
	result, _ := env["result"].(map[string]any)
	if flagged, _ := result["isError"].(bool); !flagged {
		return "", false
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return "", true
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, true
}

// TestMCPSinkListsBothToolsWhicheverIsActive covers TDD 6.10: a harness reads
// tools/list once, when it connects, and never again, so the list must name both
// tools whichever one the next turn accepts. A list that hid the summary tool at
// connect time made every summary turn fail against a live kiro-cli, because the
// harness could never call a tool it had not been told about (ANTI-PATTERNS #15).
func TestMCPSinkListsBothToolsWhicheverIsActive(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()

	for _, active := range []string{"submit_verdict", "submit_summary"} {
		sink.SetActiveTool(active)
		env, _ := post(t, sink.Endpoint(), "sess", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		result, _ := env["result"].(map[string]any)
		tools, _ := result["tools"].([]any)
		names := toolNames(tools)
		if len(names) != 2 || names[0] != "submit_verdict" || names[1] != "submit_summary" {
			t.Errorf("active=%s: tools/list = %v, want both tools", active, names)
		}
	}
}

// TestMCPSinkAcceptsTheSummaryToolAfterAConnectTimeList replays what a real
// client does: list once while the verdict tool is active, then be asked for a
// summary after the sink switches. The summary call must be accepted and
// surfaced to Await (TDD 6.10).
func TestMCPSinkAcceptsTheSummaryToolAfterAConnectTimeList(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()
	url := sink.Endpoint()

	post(t, url, "sess", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	sink.SetActiveTool("submit_summary")

	go func() {
		post(t, url, "sess",
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"submit_summary","arguments":{"summary":"3 PRs changed"}}}`)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := sink.Await(ctx)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	var parsed struct{ Summary string }
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed.Summary != "3 PRs changed" {
		t.Errorf("summary result = %q (err %v), want the submitted summary", raw, err)
	}
}

// TestMCPSinkRefusesTheInactiveToolWithAnErrorResult covers TDD 6.10: a call to
// the tool this turn is not asking for is refused with an error tool result that
// names the right tool, is never surfaced to Await, and is not a JSON-RPC error
// (which would end the harness's turn with nothing the model can act on).
func TestMCPSinkRefusesTheInactiveToolWithAnErrorResult(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()
	url := sink.Endpoint()
	sink.SetActiveTool("submit_summary")

	env, _ := post(t, url, "sess",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting","emoji":"⏳"}}}`)
	if _, isRPCError := env["error"]; isRPCError {
		t.Fatalf("a listed-but-inactive tool must not be a JSON-RPC error: %v", env["error"])
	}
	text, refused := isErrorResult(env)
	if !refused {
		t.Fatalf("calling the inactive verdict tool must return an isError tool result, got %v", env)
	}
	if !strings.Contains(text, "submit_summary") {
		t.Errorf("refusal %q should name the tool to call instead", text)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if raw, err := sink.Await(ctx); err == nil {
		t.Errorf("a refused call must not be surfaced to Await, got %q", raw)
	}

	sink.SetActiveTool("submit_verdict")
	env, _ = post(t, url, "sess",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"submit_summary","arguments":{"summary":"x"}}}`)
	if text, refused := isErrorResult(env); !refused || !strings.Contains(text, "submit_verdict") {
		t.Errorf("calling the now-inactive summary tool = %v, want a refusal naming submit_verdict", env)
	}
}
