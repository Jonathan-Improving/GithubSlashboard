package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mcpSink is a minimal Model Context Protocol server over the streamable-HTTP
// transport that exposes exactly one tool AT A TIME — either submit_verdict
// (classification, TDD 6.8) or submit_summary (TDD 6.9) — through which a
// harness returns its result for the request currently in flight. It is the
// read half of a SessionProvider: Invoke/Summarize submits the prompt after
// selecting which tool this turn offers, the harness calls that tool, and this
// sink surfaces the tool arguments to Await as the raw result for the caller.
//
// The two tools are mutually exclusive per turn (TDD 6.10): tools/list reports
// only the currently active one, so the harness is never offered a choice
// between them and left to guess which one this turn wants. Only one
// classification-or-summary is in flight at a time (a session serializes
// requests via the caller's own mutex), so a single mutable activeTool field
// is safe without its own locking. The server binds to loopback and requires
// no auth: it is reachable only from the local harness the tool spawns, and it
// never touches GitHub, so it does not widen the read-only surface (POLICY:
// GitHub read-only; the verdict/summary tools are an inbound sink, not a
// mutating call).
type mcpSink struct {
	server   *http.Server
	listener net.Listener
	url      string

	verdictTool string
	summaryTool string
	// activeTool is which of verdictTool/summaryTool is currently offered.
	// Set by the caller (via SetActiveTool) before each turn starts; read by
	// tools/list and handleToolCall. Not mutex-guarded: the caller's own
	// serialization (one turn in flight per session) is what makes this safe,
	// exactly as with the rest of the session provider's per-turn state.
	activeTool string

	mu        sync.Mutex
	sessionID string

	verdicts chan string
	ready    chan struct{}
	readyOne sync.Once
}

// mcpProtocolVersion is the protocol version the sink advertises on initialize.
// It is a fixed protocol constant of the MCP streamable-HTTP transport, not a
// tunable, so it is a named constant here rather than config.
const mcpProtocolVersion = "2025-06-18"

// newMCPSink starts a loopback MCP server exposing the verdict and summary
// tools (mutually exclusive per turn, TDD 6.10) and returns a sink bound to it.
// The caller Closes it to stop the listener. The active tool defaults to
// verdictTool, since classification is the first thing any session does.
func newMCPSink(verdictTool, summaryTool string) (*mcpSink, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mcp sink: listen: %w", err)
	}
	s := &mcpSink{
		listener:    ln,
		url:         fmt.Sprintf("http://%s/mcp", ln.Addr().String()),
		verdictTool: verdictTool,
		summaryTool: summaryTool,
		activeTool:  verdictTool,
		verdicts:    make(chan string, 1),
		ready:       make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/mcp/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.server.Serve(ln) }()
	return s, nil
}

// SetActiveTool switches which tool tools/list advertises and handleToolCall
// accepts, ahead of the next turn (TDD 6.10). The caller sets this before
// submitting the prompt for that turn.
func (s *mcpSink) SetActiveTool(name string) { s.activeTool = name }

// Endpoint reports the MCP URL the harness posts verdicts to.
func (s *mcpSink) Endpoint() string { return s.url }

// WaitReady blocks until the harness has connected and listed tools (proving
// the MCP path works end to end), or ctx expires. This is a harness-agnostic
// readiness signal: it does not depend on any harness's terminal output.
func (s *mcpSink) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("harness did not connect to the verdict sink in time")
	case <-s.ready:
		return nil
	}
}

// Await blocks until the harness calls submit_verdict or ctx is done. Any
// verdict left over from a prior request is drained by Reset semantics: the
// SessionProvider clears the harness context between requests, and the channel
// holds at most one pending verdict.
func (s *mcpSink) Await(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("timed out waiting for verdict tool call")
	case v := <-s.verdicts:
		return v, nil
	}
}

// Drain discards a pending verdict, if any, without blocking.
func (s *mcpSink) Drain() {
	select {
	case <-s.verdicts:
	default:
	}
}

// Close shuts the HTTP server down.
func (s *mcpSink) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// jsonRPCRequest is a minimal JSON-RPC 2.0 request. A request without an ID is
// a notification (no response body).
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// handleMCP implements the streamable-HTTP transport for POST /mcp: initialize,
// notifications/initialized, tools/list, tools/call, and ping.
func (s *mcpSink) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// GET (server-initiated SSE) and other methods are unused.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, r, nil, -32700, "parse error")
		return
	}

	// A message without an ID is a notification: process, return 202, no body.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		s.assignSession()
		w.Header().Set("Mcp-Session-Id", s.currentSession())
		s.writeResult(w, r, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "githubslashboard-verdict", "version": "1.0.0"},
		})
	case "ping":
		s.writeResult(w, r, req.ID, map[string]any{})
	case "tools/list":
		s.readyOne.Do(func() { close(s.ready) })
		s.writeResult(w, r, req.ID, map[string]any{"tools": []any{s.activeToolSpec()}})
	case "tools/call":
		s.handleToolCall(w, r, req)
	default:
		s.writeError(w, r, req.ID, -32601, "method not found: "+req.Method)
	}
}

// activeToolSpec returns the tools/list entry for whichever tool is currently
// active (TDD 6.10) — the harness is offered exactly one spec per turn, never
// both, so it cannot guess wrong about which one this turn wants.
func (s *mcpSink) activeToolSpec() map[string]any {
	if s.activeTool == s.summaryTool {
		return s.summaryToolSpec()
	}
	return s.verdictToolSpec()
}

// verdictToolSpec is the tools/list entry for the verdict tool. Its input
// schema mirrors the response contract (SCHEMA § Response) so the harness is
// told the exact fields to supply.
//
// bucket, action, and close_reason carry no JSON-schema enum: the legal
// vocabulary is entity-specific (a pull request and an issue do not share an
// action set, and an issue has no merged bucket), and it is supplied per request
// in the prompt file's constraints. Membership is enforced by the same validator
// the one-shot path uses (TDD 6.6), so a wrong value drives the self-correcting
// retry rather than being silently accepted here.
func (s *mcpSink) verdictToolSpec() map[string]any {
	return map[string]any{
		"name":        s.verdictTool,
		"description": "Submit the final classification verdict for the item currently under review, per this turn's instructions. Call this exactly once with the classification fields.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bucket":       map[string]any{"type": "string", "description": "one of the buckets listed in the request's constraints"},
				"action":       map[string]any{"type": "string", "description": "required only when bucket is open; one of the actions listed in the request's constraints"},
				"close_reason": map[string]any{"type": "string", "description": "required only when bucket is closed and the request's constraints list close reasons"},
				"priority":     map[string]any{"type": "string", "enum": []string{"neutral", "elevated"}},
				"companion":    map[string]any{"type": "string", "description": "a short note within the configured word bounds"},
				"emoji":        map[string]any{"type": "string", "description": "exactly one emoji character that visually summarizes the note"},
			},
			"required": []string{"bucket", "priority", "companion", "emoji"},
		},
	}
}

// summaryToolSpec is the tools/list entry for the summary tool (TDD 6.9,
// SCHEMA § Summarize contract). Its schema is deliberately minimal — one short
// sentence, no vocabulary to validate against — matching the lower stakes of a
// notification convenience versus an authoritative classification.
func (s *mcpSink) summaryToolSpec() map[string]any {
	return map[string]any{
		"name":        s.summaryTool,
		"description": "Submit your one-sentence summary for the notification described in this turn's instructions. Call this exactly once.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "one short sentence summarizing the changes described in the prompt"},
			},
			"required": []string{"summary"},
		},
	}
}

// handleToolCall receives the harness's submit_verdict call, re-encodes its
// arguments as the raw JSON verdict, and hands it to Await. The tool result
// acknowledges receipt so the harness can end its turn.
func (s *mcpSink) handleToolCall(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(w, r, req.ID, -32602, "invalid params")
		return
	}
	if params.Name != s.activeTool {
		s.writeError(w, r, req.ID, -32601, "unknown tool: "+params.Name)
		return
	}

	// The arguments object is the verdict; forward it verbatim for the shared
	// parser to validate against the enums and word bounds (TDD 6.6).
	verdict := string(params.Arguments)
	if strings.TrimSpace(verdict) == "" {
		verdict = "{}"
	}
	select {
	case s.verdicts <- verdict:
	default:
		// A verdict is already pending; replace it so the latest call wins.
		select {
		case <-s.verdicts:
		default:
		}
		s.verdicts <- verdict
	}

	s.writeResult(w, r, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "verdict recorded"}},
	})
}

func (s *mcpSink) assignSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID == "" {
		s.sessionID = fmt.Sprintf("gsb-%d", time.Now().UnixNano())
	}
}

func (s *mcpSink) currentSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// writeResult writes a JSON-RPC success response, honoring an SSE Accept.
func (s *mcpSink) writeResult(w http.ResponseWriter, r *http.Request, id json.RawMessage, result any) {
	s.writeEnvelope(w, r, map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result})
}

// writeError writes a JSON-RPC error response.
func (s *mcpSink) writeError(w http.ResponseWriter, r *http.Request, id json.RawMessage, code int, msg string) {
	s.writeEnvelope(w, r, map[string]any{
		"jsonrpc": "2.0", "id": rawOrNull(id),
		"error": map[string]any{"code": code, "message": msg},
	})
}

// writeEnvelope emits the JSON-RPC envelope as plain JSON, or as a single SSE
// event when the client's Accept header prefers text/event-stream (strict MCP
// clients). It never holds the stream open — the sink emits no server-initiated
// messages.
func (s *mcpSink) writeEnvelope(w http.ResponseWriter, r *http.Request, env map[string]any) {
	body, err := json.Marshal(env)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// rawOrNull returns the raw id or a JSON null when absent.
func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
