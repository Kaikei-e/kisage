package mcp_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/mcp"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
)

func sendRPC(t *testing.T, srv *mcp.Server, raw string) (mcp.JSONRPCResponse, map[string]any) {
	t.Helper()
	respBytes, ok := srv.HandleJSONRPC([]byte(raw))
	if !ok {
		t.Fatalf("expected JSON-RPC response for %s", raw)
	}
	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal JSONRPCResponse: %v (%s)", err, respBytes)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(respBytes, &rawMap); err != nil {
		t.Fatalf("unmarshal raw response map: %v (%s)", err, respBytes)
	}
	return resp, rawMap
}

func TestToolsCallLegacyMetaAcceptance(t *testing.T) {
	declPath, _, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
	)

	withoutMeta, withoutRaw := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kisage_plan","arguments":{"file":`+mustJSONQuote(declPath)+`}}}`,
	)
	if withoutMeta.Error != nil {
		t.Fatalf("unexpected error without _meta: %+v", withoutMeta.Error)
	}

	withMeta, withRaw := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"_meta":{"progressToken":1},"name":"kisage_plan","arguments":{"file":`+mustJSONQuote(declPath)+`}}}`,
	)
	if withMeta.Error != nil {
		t.Fatalf("unexpected error with legacy _meta: %+v", withMeta.Error)
	}

	withEmptyMeta, _ := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"_meta":{},"name":"kisage_plan","arguments":{"file":`+mustJSONQuote(declPath)+`}}}`,
	)
	if withEmptyMeta.Error != nil {
		t.Fatalf("unexpected error with empty _meta: %+v", withEmptyMeta.Error)
	}

	resWithout, ok := withoutRaw["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected object result without _meta, got %T", withoutRaw["result"])
	}
	resWith, ok := withRaw["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected object result with _meta, got %T", withRaw["result"])
	}
	if _, hasResultType := resWith["resultType"]; hasResultType {
		t.Fatalf("legacy tools/call result must not include resultType: %v", resWith)
	}
	if _, hasRespMeta := resWith["_meta"]; hasRespMeta {
		t.Fatalf("legacy tools/call result must not include _meta: %v", resWith)
	}
	if !reflect.DeepEqual(resWithout["structuredContent"], resWith["structuredContent"]) {
		t.Fatalf("structuredContent mismatch:\nwithout _meta: %#v\nwith _meta:    %#v", resWithout["structuredContent"], resWith["structuredContent"])
	}
	if !reflect.DeepEqual(resWithout, resWith) {
		t.Fatalf("legacy tools/call result mismatch:\nwithout _meta: %#v\nwith _meta:    %#v", resWithout, resWith)
	}
}

func TestToolsCallStrictParamsAndInvalidMetaRejected(t *testing.T) {
	declPath, _, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
	)

	fileJSON := mustJSONQuote(declPath)
	modernMeta := `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

	cases := []struct {
		name   string
		params string
	}{
		{
			name:   "unknown_argument_without_meta",
			params: `{"name":"kisage_plan","arguments":{"file":` + fileJSON + `,"unknownField":true}}`,
		},
		{
			name:   "unknown_argument_with_legacy_meta",
			params: `{"_meta":{"progressToken":1},"name":"kisage_plan","arguments":{"file":` + fileJSON + `,"unknownField":true}}`,
		},
		{
			name:   "unknown_argument_with_modern_meta",
			params: `{"_meta":` + modernMeta + `,"name":"kisage_plan","arguments":{"file":` + fileJSON + `,"unknownField":true}}`,
		},
		{
			name:   "unknown_top_level_task",
			params: `{"name":"kisage_plan","task":{},"arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "unknown_top_level_foo",
			params: `{"name":"kisage_plan","foo":1,"arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "unknown_top_level_inputResponses_modern",
			params: `{"_meta":` + modernMeta + `,"name":"kisage_plan","inputResponses":{},"arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "unknown_top_level_requestState_modern",
			params: `{"_meta":` + modernMeta + `,"name":"kisage_plan","requestState":"tok","arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "case_mismatched_meta_key",
			params: `{"_Meta":{},"name":"kisage_plan","arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "non_object_meta_null",
			params: `{"_meta":null,"name":"kisage_plan","arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "non_object_meta_array",
			params: `{"_meta":[],"name":"kisage_plan","arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "non_object_meta_number",
			params: `{"_meta":42,"name":"kisage_plan","arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "non_object_meta_string",
			params: `{"_meta":"invalid","name":"kisage_plan","arguments":{"file":` + fileJSON + `}}`,
		},
		{
			name:   "non_object_meta_bool",
			params: `{"_meta":true,"name":"kisage_plan","arguments":{"file":` + fileJSON + `}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, rawMap := sendRPC(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+tc.params+`}`)
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("expected -32602 for %s, got %+v", tc.name, resp)
			}
			if resp.Error.Data != nil {
				t.Fatalf("expected JSONRPCError.Data==nil when empty, got %#v", resp.Error.Data)
			}
			errObj, _ := rawMap["error"].(map[string]any)
			if _, hasData := errObj["data"]; hasData {
				t.Fatalf("expected error.data to be omitted when empty, got %v", errObj)
			}
		})
	}
}

func TestToolsCallMetaNeverInfluencesTrustBoundary(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		declPath, repo, srv := setupMCPServer(
			t,
			"@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n",
			"@lkal \"0.1\"\napp {\n  created_key = \"v1\"\n}\n",
		)
		meta := `{"actor":` + mustJSONQuote(string(actorB)) +
			`,"resource":"spoofed-res","scope":[],"file":"/spoofed.lkal"` +
			`,"io.modelcontextprotocol/clientInfo":{"name":` + mustJSONQuote(string(actorB)) + `,"version":"9.9.9"}}`
		resp, rawMap := sendRPC(
			t,
			srv,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":`+meta+`,"name":"kisage_apply","arguments":{"file":`+mustJSONQuote(declPath)+`,"actor":`+mustJSONQuote(string(actorB))+`}}}`,
		)
		if resp.Error != nil {
			t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
		}
		assertTrustBoundaryPreserved(t, repo, rawMap)
	})

	t.Run("modern", func(t *testing.T) {
		declPath, repo, srv := setupMCPServer(
			t,
			"@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n",
			"@lkal \"0.1\"\napp {\n  created_key = \"v1\"\n}\n",
		)
		meta := `{"io.modelcontextprotocol/protocolVersion":"2026-07-28"` +
			`,"io.modelcontextprotocol/clientCapabilities":{}` +
			`,"actor":` + mustJSONQuote(string(actorB)) +
			`,"resource":"spoofed-res","scope":[],"file":"/spoofed.lkal"` +
			`,"io.modelcontextprotocol/clientInfo":{"name":` + mustJSONQuote(string(actorB)) + `,"version":"9.9.9"}}`
		resp, rawMap := sendRPC(
			t,
			srv,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":`+meta+`,"name":"kisage_apply","arguments":{"file":`+mustJSONQuote(declPath)+`,"actor":`+mustJSONQuote(string(actorB))+`}}}`,
		)
		if resp.Error != nil {
			t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
		}
		assertTrustBoundaryPreserved(t, repo, rawMap)
	})
}

func assertTrustBoundaryPreserved(t *testing.T, repo *protocol.MemoryStoreRepository, rawMap map[string]any) {
	t.Helper()
	resObj, ok := rawMap["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %T", rawMap["result"])
	}
	if resObj["isError"] != false || resObj["exitCode"] != float64(cli.ExitCodeOK) {
		t.Fatalf("expected successful apply result, got %v", resObj)
	}
	sc, ok := resObj["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("expected structuredContent map, got %T", resObj["structuredContent"])
	}
	if sc["resource"] != string(testResID) {
		t.Fatalf("expected resource=%q, got %v", testResID, sc["resource"])
	}
	store, err := repo.LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := store.Get(testResID, model.MustParsePath("/app/created_key"))
	if !ok || entry.Manager != actorA || entry.Origin != ownership.OriginCreated {
		t.Fatalf("expected host-bound Actor=%q on Resource=%q, got ok=%v entry=%+v", actorA, testResID, ok, entry)
	}
}

func TestModernServerDiscoverAndLegacyProbingFallback(t *testing.T) {
	_, _, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
	)

	initResp, initRaw := sendRPC(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if initResp.Error != nil {
		t.Fatalf("legacy initialize failed: %+v", initResp.Error)
	}
	initResult := initRaw["result"].(map[string]any)
	wantServerInfo := initResult["serverInfo"].(map[string]any)

	discResp, discRaw := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
	)
	if discResp.Error != nil {
		t.Fatalf("modern server/discover failed: %+v", discResp.Error)
	}
	res, ok := discRaw["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected discover result object, got %T", discRaw["result"])
	}
	if res["resultType"] != "complete" {
		t.Fatalf("expected resultType=complete, got %v", res["resultType"])
	}
	versions, ok := res["supportedVersions"].([]any)
	if !ok || len(versions) == 0 {
		t.Fatalf("expected non-empty supportedVersions array, got %#v", res["supportedVersions"])
	}
	foundModern := false
	for _, rawVer := range versions {
		ver, ok := rawVer.(string)
		if !ok {
			t.Fatalf("supportedVersions entry must be string, got %T", rawVer)
		}
		if ver == "2026-07-28" {
			foundModern = true
		}
		probe, _ := sendRPC(
			t,
			srv,
			`{"jsonrpc":"2.0","id":99,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":`+mustJSONQuote(ver)+`,"io.modelcontextprotocol/clientCapabilities":{}}}}`,
		)
		if probe.Error != nil {
			t.Fatalf("advertised supportedVersion %q was rejected on per-request _meta: %+v", ver, probe.Error)
		}
	}
	if !foundModern {
		t.Fatalf("expected supportedVersions to contain 2026-07-28, got %v", versions)
	}
	caps, ok := res["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("expected capabilities object, got %T", res["capabilities"])
	}
	if _, hasTools := caps["tools"].(map[string]any); !hasTools {
		t.Fatalf("expected capabilities.tools object, got %#v", caps["tools"])
	}
	if ttl, ok := res["ttlMs"].(float64); !ok || ttl != 0 {
		t.Fatalf("expected ttlMs=0, got %#v", res["ttlMs"])
	}
	if res["cacheScope"] != "public" {
		t.Fatalf("expected cacheScope=public, got %#v", res["cacheScope"])
	}
	resMeta, ok := res["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected _meta object on discover result, got %T", res["_meta"])
	}
	if !reflect.DeepEqual(resMeta["io.modelcontextprotocol/serverInfo"], wantServerInfo) {
		t.Fatalf("expected _meta serverInfo %#v, got %#v", wantServerInfo, resMeta["io.modelcontextprotocol/serverInfo"])
	}

	for _, legacyDiscover := range []string{
		`{"jsonrpc":"2.0","id":10,"method":"server/discover"}`,
		`{"jsonrpc":"2.0","id":11,"method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":12,"method":"server/discover","params":{"_meta":{}}}`,
		`{"jsonrpc":"2.0","id":13,"method":"server/discover","params":{"_meta":123}}`,
	} {
		probeResp, _ := sendRPC(t, srv, legacyDiscover)
		if probeResp.Error == nil || probeResp.Error.Code != -32602 {
			t.Fatalf("expected -32602 for legacy server/discover (%s), got %+v", legacyDiscover, probeResp)
		}
	}
}

func TestModernVersionNegotiationAndCapabilitiesValidation(t *testing.T) {
	declPath, _, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
	)

	for _, req := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`,
	} {
		resp, rawMap := sendRPC(t, srv, req)
		if resp.Error == nil || resp.Error.Code != -32022 || resp.Error.Message != "Unsupported protocol version" {
			t.Fatalf("expected -32022 Unsupported protocol version for %s, got %+v", req, resp.Error)
		}
		if resp.Error.Data == nil {
			t.Fatalf("expected non-nil JSONRPCError.Data on -32022")
		}
		errObj, _ := rawMap["error"].(map[string]any)
		dataObj, ok := errObj["data"].(map[string]any)
		if !ok {
			t.Fatalf("expected error.data object, got %#v", errObj["data"])
		}
		if dataObj["requested"] != "1900-01-01" {
			t.Fatalf("expected data.requested=1900-01-01, got %#v", dataObj["requested"])
		}
		supported, ok := dataObj["supported"].([]any)
		if !ok || len(supported) == 0 {
			t.Fatalf("expected non-empty data.supported array, got %#v", dataObj["supported"])
		}
		for _, rawSup := range supported {
			sup, ok := rawSup.(string)
			if !ok {
				t.Fatalf("expected string in data.supported, got %T", rawSup)
			}
			probe, _ := sendRPC(
				t,
				srv,
				`{"jsonrpc":"2.0","id":9,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":`+mustJSONQuote(sup)+`,"io.modelcontextprotocol/clientCapabilities":{}}}}`,
			)
			if probe.Error != nil {
				t.Fatalf("supported version %q from -32022 failed on retry: %+v", sup, probe.Error)
			}
		}
	}

	for _, badVer := range []string{`null`, `123`, `true`, `[]`, `{}`} {
		resp, _ := sendRPC(
			t,
			srv,
			`{"jsonrpc":"2.0","id":4,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":`+badVer+`,"io.modelcontextprotocol/clientCapabilities":{}}}}`,
		)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("expected -32602 for non-string protocolVersion %s, got %+v", badVer, resp)
		}
	}

	for _, missingCapsMethod := range []string{
		`{"jsonrpc":"2.0","id":5,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"},"name":"kisage_plan","arguments":{"file":` + mustJSONQuote(declPath) + `}}}`,
	} {
		resp, _ := sendRPC(t, srv, missingCapsMethod)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("expected -32602 when clientCapabilities is missing (%s), got %+v", missingCapsMethod, resp)
		}
	}

	for _, badCaps := range []string{`null`, `[]`, `"caps"`, `123`, `false`} {
		resp, _ := sendRPC(
			t,
			srv,
			`{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":`+badCaps+`}}}`,
		)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("expected -32602 for non-object clientCapabilities %s, got %+v", badCaps, resp)
		}
	}
}

func TestModernMethodsToolsListToolsCallAndRejectedLegacyMethods(t *testing.T) {
	declPath, _, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n",
		"@lkal \"0.1\"\napp {\n  k = \"v1\"\n}\n",
	)

	modernMeta := `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	wantServerInfo := map[string]any{"name": "kisage", "version": "0.1.2"}

	_, legacyListRaw := sendRPC(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	legacyTools := legacyListRaw["result"].(map[string]any)["tools"]

	listResp, listRaw := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":`+modernMeta+`}}`,
	)
	if listResp.Error != nil {
		t.Fatalf("modern tools/list failed: %+v", listResp.Error)
	}
	listRes := listRaw["result"].(map[string]any)
	if listRes["resultType"] != "complete" {
		t.Fatalf("expected modern tools/list resultType=complete, got %#v", listRes["resultType"])
	}
	if ttl, ok := listRes["ttlMs"].(float64); !ok || ttl != 0 {
		t.Fatalf("expected modern tools/list ttlMs=0, got %#v", listRes["ttlMs"])
	}
	if listRes["cacheScope"] != "public" {
		t.Fatalf("expected modern tools/list cacheScope=public, got %#v", listRes["cacheScope"])
	}
	if !reflect.DeepEqual(listRes["tools"], legacyTools) {
		t.Fatalf("modern tools/list tools mismatch")
	}
	listMeta, ok := listRes["_meta"].(map[string]any)
	if !ok || !reflect.DeepEqual(listMeta["io.modelcontextprotocol/serverInfo"], wantServerInfo) {
		t.Fatalf("expected modern tools/list _meta serverInfo %#v, got %#v", wantServerInfo, listRes["_meta"])
	}

	_, legacyCallRaw := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"kisage_plan","arguments":{"file":`+mustJSONQuote(declPath)+`}}}`,
	)
	legacyCallRes := legacyCallRaw["result"].(map[string]any)

	callResp, callRaw := sendRPC(
		t,
		srv,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"_meta":`+modernMeta+`,"name":"kisage_plan","arguments":{"file":`+mustJSONQuote(declPath)+`}}}`,
	)
	if callResp.Error != nil {
		t.Fatalf("modern tools/call failed: %+v", callResp.Error)
	}
	callRes := callRaw["result"].(map[string]any)
	if callRes["resultType"] != "complete" {
		t.Fatalf("expected modern tools/call resultType=complete, got %#v", callRes["resultType"])
	}
	callMeta, ok := callRes["_meta"].(map[string]any)
	if !ok || !reflect.DeepEqual(callMeta["io.modelcontextprotocol/serverInfo"], wantServerInfo) {
		t.Fatalf("expected modern tools/call _meta serverInfo %#v, got %#v", wantServerInfo, callRes["_meta"])
	}
	for _, field := range []string{"content", "structuredContent", "exitCode", "isError"} {
		if !reflect.DeepEqual(callRes[field], legacyCallRes[field]) {
			t.Fatalf("modern tools/call field %q mismatch:\nmodern: %#v\nlegacy: %#v", field, callRes[field], legacyCallRes[field])
		}
	}

	for _, disallowedMethod := range []string{"initialize", "ping", "resources/list"} {
		resp, _ := sendRPC(
			t,
			srv,
			`{"jsonrpc":"2.0","id":5,"method":`+mustJSONQuote(disallowedMethod)+`,"params":{"_meta":`+modernMeta+`}}`,
		)
		if resp.Error == nil || resp.Error.Code != -32601 {
			t.Fatalf("expected -32601 for modern method %q, got %+v", disallowedMethod, resp)
		}
	}

	streamInput := strings.Join([]string{
		`{"jsonrpc":"2.0","id":10,"method":"server/discover","params":{"_meta":` + modernMeta + `}}`,
		`{"jsonrpc":"2.0","id":11,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":12,"method":"ping","params":{"_meta":` + modernMeta + `}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := srv.ServeStream(strings.NewReader(streamInput), &out); err != nil {
		t.Fatalf("ServeStream failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 stream responses, got %d", len(lines))
	}
	var r10, r11, r12 mcp.JSONRPCResponse
	_ = json.Unmarshal([]byte(lines[0]), &r10)
	_ = json.Unmarshal([]byte(lines[1]), &r11)
	_ = json.Unmarshal([]byte(lines[2]), &r12)
	if r10.Error != nil || r11.Error != nil || r12.Error == nil || r12.Error.Code != -32601 {
		t.Fatalf("unexpected mixed-era stream responses: r10=%+v r11=%+v r12=%+v", r10, r11, r12)
	}
}
