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

	// MaxJSONRPCLineBytes bounds a single newline-delimited JSON-RPC 2.0 frame on stdio.
	MaxJSONRPCLineBytes = 4 << 20
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
	if !isValidJSONRPCID(req.ID) {
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      json.RawMessage("null"),
			Error:   &JSONRPCError{Code: rpcInvalidReq, Message: "invalid JSON-RPC 2.0 request id"},
		}), true
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
					"version": "0.1.1",
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
		callReq, err := decodeCallToolParams(req.Params)
		if err != nil {
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
		line, tooLong, readErr := readBoundedLine(reader, MaxJSONRPCLineBytes)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if tooLong {
			resp := marshalRPCResponse(JSONRPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      json.RawMessage("null"),
				Error:   &JSONRPCError{Code: rpcInvalidReq, Message: "JSON-RPC frame exceeds maximum allowed size"},
			})
			if _, err := w.Write(append(resp, '\n')); err != nil {
				return err
			}
			if readErr == io.EOF {
				return nil
			}
			continue
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

func readBoundedLine(r *bufio.Reader, maxBytes int) ([]byte, bool, error) {
	var buf []byte
	tooLong := false
	for {
		frag, isPrefix, err := r.ReadLine()
		if !tooLong {
			if len(buf)+len(frag) > maxBytes {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, frag...)
			}
		}
		if !isPrefix || err != nil {
			return buf, tooLong, err
		}
	}
}

func isValidJSONRPCID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	switch trimmed[0] {
	case '"':
		var s string
		return json.Unmarshal(trimmed, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&n); err != nil {
			return false
		}
		return dec.Decode(new(any)) == io.EOF
	default:
		return false
	}
}

func decodeCallToolParams(raw json.RawMessage) (CallToolRequest, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '{' {
		return CallToolRequest{}, io.ErrUnexpectedEOF
	}
	var callReq CallToolRequest
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&callReq); err != nil {
		return CallToolRequest{}, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return CallToolRequest{}, io.ErrUnexpectedEOF
	}
	return callReq, nil
}

func normalizeID(id json.RawMessage) json.RawMessage {
	if !isValidJSONRPCID(id) {
		return json.RawMessage("null")
	}
	return id
}

func marshalRPCResponse(resp JSONRPCResponse) []byte {
	b, _ := json.Marshal(resp)
	return b
}
