package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

const (
	jsonRPCVersion  = "2.0"
	mcpProtocolVer  = "2025-11-25"
	rpcParseError   = -32700
	rpcInvalidReq   = -32600
	rpcMethodNotFnd = -32601
	rpcInvalidParam = -32602
)

// JSONRPCRequest represents a JSON-RPC 2.0 request or notification over MCP stdio transport.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response over MCP stdio transport.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// HandleJSONRPC processes a single JSON-RPC 2.0 message and returns the serialized JSON response
// (or hasResponse == false for notifications).
func (s *Server) HandleJSONRPC(raw []byte) ([]byte, bool) {
	var req JSONRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &JSONRPCError{Code: rpcParseError, Message: "invalid JSON-RPC payload"},
		}), true
	}
	if req.JSONRPC != jsonRPCVersion || req.Method == "" {
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      normalizeID(req.ID),
			Error:   &JSONRPCError{Code: rpcInvalidReq, Message: "invalid JSON-RPC 2.0 request"},
		}), true
	}
	// Notifications have no ID and produce no response.
	if len(req.ID) == 0 {
		return nil, false
	}

	switch req.Method {
	case "initialize":
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": mcpProtocolVer,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "kisage",
					"version": "0.1.0",
				},
			},
		}), true
	case "ping":
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result:  map[string]any{},
		}), true
	case "tools/list":
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.ListTools(),
			},
		}), true
	case "tools/call":
		var callReq CallToolRequest
		if err := json.Unmarshal(req.Params, &callReq); err != nil {
			return marshalRPCResponse(JSONRPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      req.ID,
				Error:   &JSONRPCError{Code: rpcInvalidParam, Message: "invalid tools/call params: " + err.Error()},
			}), true
		}
		res := s.CallTool(callReq)
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result:  res,
		}), true
	default:
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &JSONRPCError{Code: rpcMethodNotFnd, Message: "method not found: " + req.Method},
		}), true
	}
}

// ServeStream reads newline-delimited JSON-RPC 2.0 messages from r and writes responses to w.
func (s *Server) ServeStream(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if len(line) == 0 {
			if readErr == io.EOF {
				return nil
			}
			continue
		}
		if len(bytes.TrimSpace(line)) > 0 {
			resp, ok := s.HandleJSONRPC(line)
			if ok {
				if _, err := w.Write(append(resp, '\n')); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
	}
}

func normalizeID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func marshalRPCResponse(resp JSONRPCResponse) []byte {
	b, _ := json.Marshal(resp)
	return b
}
