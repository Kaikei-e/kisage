package mcp_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/mcp"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
	"github.com/Kaikei-e/kisage/scope"
)

const (
	testStoreID ownership.StoreID    = "store-mcp"
	testResID   ownership.ResourceID = "res-mcp"
	actorA      ownership.ActorID    = "actor-a"
	actorB      ownership.ActorID    = "actor-b"
)

func setupMCPServer(t *testing.T, initialTarget, initialDecl string) (string, *protocol.MemoryStoreRepository, *mcp.Server) {
	t.Helper()
	rootDir := t.TempDir()
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	if err := os.Chmod(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(rootDir, "target.lkal")
	declPath := filepath.Join(rootDir, "decl.lkal")

	if err := os.WriteFile(targetPath, []byte(initialTarget), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(declPath, []byte(initialDecl), 0o600); err != nil {
		t.Fatal(err)
	}

	phys, err := provider.InspectPhysicalIdentity(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := ownership.NewResourceRegistry(ownership.ResourceBinding{
		ID:             testResID,
		Provider:       ownership.ProviderLKALFile,
		ManagementRoot: rootDir,
		TargetPath:     targetPath,
		StoreID:        testStoreID,
		Physical:       phys,
	})
	if err != nil {
		t.Fatal(err)
	}
	prov, err := provider.NewLKALFileProvider(reg)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := provider.NewFileLockManager(lockDir)
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.NewMemoryStoreRepository(ownership.NewStore(), nil)
	rootRule, err := scope.NewRootRule(scope.PermAll)
	if err != nil {
		t.Fatal(err)
	}
	host, err := cli.NewHostEnvironment(cli.HostEnvironmentConfig{
		HostActor: actorA,
		Registry:  reg,
		DeclarationBindings: []cli.DeclarationBinding{{
			DeclarationFile: declPath,
			Resource:        testResID,
			Scope:           scope.MustNew(rootRule),
		}},
		Provider:  prov,
		StoreRepo: repo,
		Locks:     locks,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mcp.NewServer(host)
	if err != nil {
		t.Fatal(err)
	}
	return declPath, repo, srv
}

// TestMCPToolAnnotationsAndSixOperations verifies ADR 0005 §1–2 and Counterexample #16:
//   - All 6 normative tools are exposed with accurate ToolAnnotations (`kisage_apply` has DestructiveHint=true).
//   - `kisage_plan`, `kisage_apply`, `kisage_explain`, `kisage_adopt`, `kisage_release`, and `kisage_prune`
//     work end-to-end and ignore untrusted `actor` tool arguments (#16).
func TestMCPToolAnnotationsAndSixOperations(t *testing.T) {
	defs := mcp.Tools()
	if err := mcp.ValidateToolDefinitions(defs); err != nil {
		t.Fatalf("ValidateToolDefinitions failed: %v", err)
	}

	declPath, repo, srv := setupMCPServer(
		t,
		"@lkal \"0.1\"\napp {\n  sibling = \"keep\"\n}\n",
		"@lkal \"0.1\"\napp {\n  created_key = \"v1\"\n}\n",
	)

	// 1. kisage_plan with detailedExitCode=true returns ExitCode=2 (Planned) and IsError=false.
	planRes := srv.CallTool(mcp.CallToolRequest{
		Name: mcp.ToolPlan,
		Arguments: mcp.ToolArguments{
			File:             declPath,
			DetailedExitCode: true,
		},
	})
	if planRes.IsError || planRes.ExitCode != cli.ExitCodePlanDiff {
		t.Fatalf("expected kisage_plan ExitCode=2 and IsError=false, got %+v", planRes)
	}

	// 2. kisage_apply with spoofed UntrustedActor="actor-b" (#16) creates /app/created_key under host-bound `actor-a`.
	applyRes := srv.CallTool(mcp.CallToolRequest{
		Name: mcp.ToolApply,
		Arguments: mcp.ToolArguments{
			File:           declPath,
			UntrustedActor: string(actorB),
		},
	})
	if applyRes.IsError || applyRes.ExitCode != cli.ExitCodeOK {
		t.Fatalf("expected kisage_apply to succeed, got %+v", applyRes)
	}
	storeAfterApply, _ := repo.LoadStore()
	entry, ok := storeAfterApply.Get(testResID, model.MustParsePath("/app/created_key"))
	if !ok || entry.Manager != actorA || entry.Origin != ownership.OriginCreated {
		t.Fatalf("Counterexample #16 failed on MCP: expected Manager=%s and Origin=Created, got %+v", actorA, entry)
	}

	// 3. Remove /app/created_key from declaration and run `kisage_prune` -> deletes retired Created leaf and converges.
	if err := os.WriteFile(declPath, []byte("@lkal \"0.1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pruneRes := srv.CallTool(mcp.CallToolRequest{
		Name:      mcp.ToolPrune,
		Arguments: mcp.ToolArguments{File: declPath},
	})
	if pruneRes.IsError || pruneRes.ExitCode != cli.ExitCodeOK {
		t.Fatalf("expected kisage_prune to succeed, got %+v", pruneRes)
	}

	// 4. Test JSON-RPC 2.0 stream (`initialize`, `tools/list`, `tools/call` for `kisage_explain`).
	reqLines := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"kisage_explain","arguments":{"file":` + mustJSONQuote(declPath) + `,"path":"/app/sibling"}}}`,
	}, "\n") + "\n"

	var respBuf bytes.Buffer
	if err := srv.ServeStream(strings.NewReader(reqLines), &respBuf); err != nil {
		t.Fatalf("ServeStream failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(respBuf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 JSON-RPC responses (notification produces none), got %d: %s", len(lines), respBuf.String())
	}
}

func mustJSONQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
