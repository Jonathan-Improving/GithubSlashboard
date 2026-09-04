package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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

	// tools/list: advertises the single active tool (verdict by default).
	env, _ = post(t, url, sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ = env["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools/list returned %d tools, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "submit_verdict" {
		t.Errorf("tool name = %v", tool["name"])
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

// TestMCPSinkToolsAreMutuallyExclusive covers TDD 6.10: tools/list reports
// only whichever tool is currently active, never both, and switching the
// active tool immediately changes what tools/list reports and what
// tools/call accepts — proving genuine per-turn exclusivity, not just prompt
// wording.
func TestMCPSinkToolsAreMutuallyExclusive(t *testing.T) {
	sink, err := newMCPSink("submit_verdict", "submit_summary")
	if err != nil {
		t.Fatalf("newMCPSink: %v", err)
	}
	defer sink.Close()
	url := sink.Endpoint()

	// Default active tool is the verdict tool.
	env, _ := post(t, url, "sess", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := env["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools/list returned %d tools, want exactly 1", len(tools))
	}
	if tool, _ := tools[0].(map[string]any); tool["name"] != "submit_verdict" {
		t.Errorf("default active tool = %v, want submit_verdict", tool["name"])
	}

	// Calling the inactive (summary) tool while verdict is active is rejected.
	env, _ = post(t, url, "sess",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"submit_summary","arguments":{"summary":"x"}}}`)
	if _, ok := env["error"]; !ok {
		t.Error("calling the inactive tool should return a JSON-RPC error")
	}

	// Switch to summary mode: tools/list now reports only submit_summary.
	sink.SetActiveTool("submit_summary")
	env, _ = post(t, url, "sess", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	result, _ = env["result"].(map[string]any)
	tools, _ = result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools/list returned %d tools after switch, want exactly 1", len(tools))
	}
	if tool, _ := tools[0].(map[string]any); tool["name"] != "submit_summary" {
		t.Errorf("active tool after switch = %v, want submit_summary", tool["name"])
	}

	// Now the verdict tool (formerly active) is the one rejected.
	env, _ = post(t, url, "sess",
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting","emoji":"⏳"}}}`)
	if _, ok := env["error"]; !ok {
		t.Error("calling the now-inactive verdict tool should return a JSON-RPC error")
	}

	// The active (summary) tool call succeeds and is surfaced to Await.
	go func() {
		post(t, url, "sess",
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"submit_summary","arguments":{"summary":"3 PRs changed"}}}`)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := sink.Await(ctx)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	var parsed struct{ Summary string }
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("summary result did not parse: %v (raw=%s)", err, raw)
	}
	if parsed.Summary != "3 PRs changed" {
		t.Errorf("summary = %q, want %q", parsed.Summary, "3 PRs changed")
	}
}
