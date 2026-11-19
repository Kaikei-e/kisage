package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	jsonRPCVersion            = "2.0"
	mcpProtocolVer            = "2025-11-25"
	mcpModernProtocolVer      = "2026-07-28"
	mcpServerName             = "kisage"
	mcpServerVersion          = "0.1.2"
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaKeyServerInfo         = "io.modelcontextprotocol/serverInfo"

	rpcParseError     = -32700
	rpcInvalidReq     = -32600
	rpcMethodNotFnd   = -32601
	rpcInvalidParam   = -32602
	rpcUnsupportedVer = -32022

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
	Data    any    `json:"data,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response over MCP stdio transport.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type modernCallToolResult struct {
	ResultType string         `json:"resultType"`
	Meta       map[string]any `json:"_meta"`
	CallToolResult
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

	modern, metaErr := inspectModernMeta(req.Params)
	if metaErr != nil {
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   metaErr,
		}), true
	}
	if modern {
		return s.handleModernRPC(req), true
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
				"serverInfo": serverInfoMap(),
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
	case "server/discover":
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &JSONRPCError{Code: rpcInvalidParam, Message: "invalid server/discover params: missing required _meta protocolVersion"},
		}), true
	default:
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &JSONRPCError{Code: rpcMethodNotFnd, Message: "method not found: " + req.Method},
		}), true
	}
}

func (s *Server) handleModernRPC(req JSONRPCRequest) []byte {
	switch req.Method {
	case "server/discover":
		if err := decodeDiscoverParams(req.Params); err != nil {
			return marshalRPCResponse(JSONRPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      req.ID,
				Error:   &JSONRPCError{Code: rpcInvalidParam, Message: "invalid server/discover params: " + err.Error()},
			})
		}
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: map[string]any{
				"resultType":        "complete",
				"supportedVersions": []string{mcpModernProtocolVer},
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"ttlMs":      0,
				"cacheScope": "public",
				"_meta":      modernResultMeta(),
			},
		})
	case "tools/list":
		if err := decodeModernListToolsParams(req.Params); err != nil {
			return marshalRPCResponse(JSONRPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      req.ID,
				Error:   &JSONRPCError{Code: rpcInvalidParam, Message: "invalid tools/list params: " + err.Error()},
			})
		}
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: map[string]any{
				"resultType": "complete",
				"tools":      s.ListTools(),
				"ttlMs":      0,
				"cacheScope": "public",
				"_meta":      modernResultMeta(),
			},
		})
	case "tools/call":
		callReq, err := decodeCallToolParams(req.Params)
		if err != nil {
			return marshalRPCResponse(JSONRPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      req.ID,
				Error:   &JSONRPCError{Code: rpcInvalidParam, Message: "invalid tools/call params: " + err.Error()},
			})
		}
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: modernCallToolResult{
				ResultType:     "complete",
				Meta:           modernResultMeta(),
				CallToolResult: s.CallTool(callReq),
			},
		})
	default:
		return marshalRPCResponse(JSONRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &JSONRPCError{Code: rpcMethodNotFnd, Message: "method not found: " + req.Method},
		})
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

type callToolParams struct {
	Meta      json.RawMessage `json:"_meta"`
	Name      string          `json:"name"`
	Arguments ToolArguments   `json:"arguments"`
}

// inspectModernMeta selects the modern (2026-07-28) era iff params._meta is a JSON object
// containing io.modelcontextprotocol/protocolVersion, and validates its required envelope fields.
func inspectModernMeta(rawParams json.RawMessage) (bool, *JSONRPCError) {
	top, ok := parseJSONObject(rawParams)
	if !ok {
		return false, nil
	}
	rawMeta, hasMeta := top["_meta"]
	if !hasMeta {
		return false, nil
	}
	metaMap, ok := parseJSONObject(rawMeta)
	if !ok {
		return false, nil
	}
	rawVer, hasVer := metaMap[metaKeyProtocolVersion]
	if !hasVer {
		return false, nil
	}

	ver, ok := parseJSONString(rawVer)
	if !ok {
		return true, &JSONRPCError{
			Code:    rpcInvalidParam,
			Message: "invalid _meta: io.modelcontextprotocol/protocolVersion must be a string",
		}
	}
	if ver != mcpModernProtocolVer {
		return true, &JSONRPCError{
			Code:    rpcUnsupportedVer,
			Message: "Unsupported protocol version",
			Data: map[string]any{
				"supported": []string{mcpModernProtocolVer},
				"requested": ver,
			},
		}
	}
	rawCaps, hasCaps := metaMap[metaKeyClientCapabilities]
	if !hasCaps {
		return true, &JSONRPCError{
			Code:    rpcInvalidParam,
			Message: "invalid _meta: missing io.modelcontextprotocol/clientCapabilities",
		}
	}
	if _, ok := parseJSONObject(rawCaps); !ok {
		return true, &JSONRPCError{
			Code:    rpcInvalidParam,
			Message: "invalid _meta: io.modelcontextprotocol/clientCapabilities must be an object",
		}
	}
	return true, nil
}

func decodeDiscoverParams(raw json.RawMessage) error {
	top, ok := parseJSONObject(raw)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	for k := range top {
		if k != "_meta" {
			return errors.New("unknown field " + k)
		}
	}
	return nil
}

func decodeModernListToolsParams(raw json.RawMessage) error {
	top, ok := parseJSONObject(raw)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	for k, v := range top {
		switch k {
		case "_meta":
		case "cursor":
			if _, ok := parseJSONString(v); !ok {
				return errors.New("cursor must be a string")
			}
		default:
			return errors.New("unknown field " + k)
		}
	}
	return nil
}

func decodeCallToolParams(raw json.RawMessage) (CallToolRequest, error) {
	trimmed := bytes.TrimSpace(raw)
	top, ok := parseJSONObject(trimmed)
	if !ok {
		return CallToolRequest{}, io.ErrUnexpectedEOF
	}
	for k := range top {
		if k != "_meta" && k != "name" && k != "arguments" {
			return CallToolRequest{}, errors.New("unknown field " + k)
		}
	}
	if rawMeta, hasMeta := top["_meta"]; hasMeta {
		if _, ok := parseJSONObject(rawMeta); !ok {
			return CallToolRequest{}, errors.New("_meta must be a JSON object")
		}
	}
	var params callToolParams
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&params); err != nil {
		return CallToolRequest{}, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return CallToolRequest{}, io.ErrUnexpectedEOF
	}
	return CallToolRequest{
		Name:      params.Name,
		Arguments: params.Arguments,
	}, nil
}

func parseJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

func parseJSONString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", false
	}
	return s, true
}

func serverInfoMap() map[string]any {
	return map[string]any{
		"name":    mcpServerName,
		"version": mcpServerVersion,
	}
}

func modernResultMeta() map[string]any {
	return map[string]any{
		metaKeyServerInfo: serverInfoMap(),
	}
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
