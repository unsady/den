package claude

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
)

// MCP serves the tools of session id to its claude: fx's tools, which
// wait here for fx to run them. Only claude has the secret; the agent can
// reach the proxy too, and must not answer for fx.
func (c *Claude) MCP(w http.ResponseWriter, r *http.Request, id string) {
	want := "Bearer " + c.secret()
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		return
	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	c.bridge.mu.Lock()
	s := c.bridge.sessions[id]
	c.bridge.mu.Unlock()
	if s == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}

	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			Meta            struct {
				ToolUseID string `json:"claudecode/toolUseId"`
			} `json:"_meta"`
		} `json:"params"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "not JSON-RPC", http.StatusBadRequest)
		return
	}
	// A notification gets no answer.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": req.Params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "den", "version": "0"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		tools := make([]any, len(s.tools))
		for i, t := range s.tools {
			schema := t.Function.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools[i] = map[string]any{"name": t.Function.Name, "description": t.Function.Description, "inputSchema": schema}
		}
		result = map[string]any{"tools": tools}
	case "tools/call":
		id := req.Params.Meta.ToolUseID
		if id == "" {
			rpcError(w, req.ID, -32602, "the call has no tool use id")
			return
		}
		s.mu.Lock()
		answer := s.slot(id)
		s.mu.Unlock()

		select {
		case text := <-answer:
			s.mu.Lock()
			delete(s.results, id)
			s.mu.Unlock()
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}}
		case <-s.done:
			rpcError(w, req.ID, -32000, "den ended the session")
			return
		case <-r.Context().Done():
			return
		}
	default:
		// Claude asks server/discover first and falls back on this.
		rpcError(w, req.ID, -32601, "method not found")
		return
	}
	respond(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func rpcError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	respond(w, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func respond(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}
