package mcp_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/mcp"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// #36-38, #50: explicitly exercise adopt/release at the MCP boundary, including
// stale REV, no target writes, non-promotion and reacquisition after release.
func TestMCPExplicitResolutionAndStructuredInputErrors(t *testing.T) {
	decl, repo, server := setupMCPServer(t, "@lkal \"0.1\"\nx = \"legacy\"\n", "@lkal \"0.1\"\nx = \"desired\"\n")
	call := func(name string, args mcp.ToolArguments) mcp.CallToolResult {
		t.Helper()
		return server.CallTool(mcp.CallToolRequest{Name: name, Arguments: args})
	}
	explain := call(mcp.ToolExplain, mcp.ToolArguments{File: decl, Path: "/x"})
	doc := explain.StructuredContent.(*cli.CommandDocument)
	rev := doc.Explanation.OwnerRevision
	args := mcp.ToolArguments{File: decl, Path: "/x", Expect: doc.Explanation.ConditionToken, OwnerRevision: &rev}
	adopt := call(mcp.ToolAdopt, args)
	if adopt.IsError || !adopt.StructuredContent.(*cli.CommandDocument).Resolution.Persisted {
		t.Fatalf("adopt failed: %+v", adopt)
	}
	store, _ := repo.LoadStore()
	entry, _ := store.Get(testResID, model.MustParsePath("/x"))
	if entry.Origin != ownership.OriginModified {
		t.Fatalf("adopt acquired deletion rights: %+v", entry)
	}
	stale := call(mcp.ToolRelease, args)
	if !stale.IsError || stale.StructuredContent.(*cli.CommandDocument).Summary.Execution != "Blocked" {
		t.Fatalf("stale revision accepted: %+v", stale)
	}
	explain = call(mcp.ToolExplain, mcp.ToolArguments{File: decl, Path: "/x"})
	doc = explain.StructuredContent.(*cli.CommandDocument)
	rev = doc.Explanation.OwnerRevision
	args.Expect = doc.Explanation.ConditionToken
	release := call(mcp.ToolRelease, args)
	released := release.StructuredContent.(*cli.CommandDocument)
	if release.IsError || !released.Resolution.Persisted || !released.Resolution.RemanageOnNextApply {
		t.Fatalf("release did not preserve the declaration diagnostic: %+v", released)
	}
	explain = call(mcp.ToolExplain, mcp.ToolArguments{File: decl, Path: "/x"})
	doc = explain.StructuredContent.(*cli.CommandDocument)
	if !strings.Contains(doc.Explanation.Observed, "legacy") || doc.Explanation.OwnerManager != "" {
		t.Fatalf("resolution changed environment or retained ownership: %+v", doc.Explanation)
	}
	raw := []byte("@lkal \"0.1\"\nx {}\n")
	if err := os.WriteFile(decl, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	failed := call(mcp.ToolPlan, mcp.ToolArguments{File: decl, DetailedExitCode: true})
	planDoc, ok := failed.StructuredContent.(*cli.PlanDocument)
	if !ok || !failed.IsError || failed.ExitCode != cli.ExitCodeError || planDoc.DeclarationDigest != cli.ComputeDeclarationDigest(raw) || planDoc.Plans.Apply.Summary.Diagnostics[0].Code != "K102" {
		t.Fatalf("input error lost structured context: %+v", failed)
	}
	var content cli.PlanDocument
	if err := json.Unmarshal([]byte(failed.Content[0].Text), &content); err != nil || content.DeclarationDigest != planDoc.DeclarationDigest {
		t.Fatalf("MCP text and structured documents disagree: %v", err)
	}
}

func TestMCPStreamAcceptsLargeMessagesAndFinalUnterminatedLine(t *testing.T) {
	_, _, server := setupMCPServer(t, "@lkal \"0.1\"\n", "@lkal \"0.1\"\n")
	request := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"padding":"` + strings.Repeat("a", 128*1024) + `"}}`
	var out bytes.Buffer
	if err := server.ServeStream(strings.NewReader(request+"\n\n"+`{"jsonrpc":"2.0","id":2,"method":"ping"}`), &out); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected two responses, got %d", len(lines))
	}
	for _, line := range lines {
		var response mcp.JSONRPCResponse
		if err := json.Unmarshal(line, &response); err != nil || response.Error != nil {
			t.Fatalf("invalid ping response: %s (%v)", line, err)
		}
	}
}
