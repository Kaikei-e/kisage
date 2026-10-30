package mcp_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Kaikei-e/kisage/mcp"
)

func TestSecurityRegression_GC14_JSONRPCHardening(t *testing.T) {
	declPath, _, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
	)

	// 1. Invalid IDs ("id": null, array, object, bool) must return -32600 Invalid Request.
	for _, badID := range []string{`null`, `[1]`, `{"a":1}`, `true`} {
		raw := `{"jsonrpc":"2.0","id":` + badID + `,"method":"ping"}`
		respBytes, ok := srv.HandleJSONRPC([]byte(raw))
		if !ok {
			t.Fatalf("expected error response for bad id %s", badID)
		}
		var resp mcp.JSONRPCResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != -32600 {
			t.Fatalf("expected -32600 for bad id %s, got %+v", badID, resp)
		}
	}

	// 2. tools/call with null params or unknown fields must return -32602 Invalid Params.
	for _, badParams := range []string{
		`null`,
		`{"name":"kisage_plan","arguments":{"file":` + mustJSONQuote(declPath) + `,"unknownField":true}}`,
	} {
		raw := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + badParams + `}`
		respBytes, ok := srv.HandleJSONRPC([]byte(raw))
		if !ok {
			t.Fatalf("expected error response for bad params %s", badParams)
		}
		var resp mcp.JSONRPCResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("expected -32602 for bad params %s, got %+v", badParams, resp)
		}
	}

	// 3. ServeStream rejects frames exceeding MaxJSONRPCLineBytes with -32600 and continues serving subsequent valid frames.
	oversized := `{"jsonrpc":"2.0","id":1,"method":"ping","params":"` + strings.Repeat("A", mcp.MaxJSONRPCLineBytes+1024) + `"}` + "\n"
	valid := `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
	var out bytes.Buffer
	if err := srv.ServeStream(strings.NewReader(oversized+valid), &out); err != nil {
		t.Fatalf("ServeStream failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(lines))
	}
	var r1, r2 mcp.JSONRPCResponse
	_ = json.Unmarshal([]byte(lines[0]), &r1)
	_ = json.Unmarshal([]byte(lines[1]), &r2)
	if r1.Error == nil || r1.Error.Code != -32600 {
		t.Fatalf("expected first response to be -32600, got %+v", r1)
	}
	if r2.Error != nil || string(r2.ID) != "2" {
		t.Fatalf("expected second response to succeed for id=2, got %+v", r2)
	}
}
