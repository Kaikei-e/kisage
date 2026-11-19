package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/host"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
	"github.com/Kaikei-e/kisage/provider"
)

// The child enters the actual main function, including environment configuration,
// stdio and os.Exit. The parent observes real process exit codes and disk state.
func TestCommandProcess(t *testing.T) {
	if os.Getenv("KISAGE_TEST_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"kisage"}, os.Args[i+1:]...)
			main()
		}
	}
	t.Fatal("missing child arguments")
}

func fixture(t *testing.T) (host.Config, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "store")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	target, declaration := filepath.Join(dir, "target.lkal"), filepath.Join(dir, "desired.lkal")
	writeFile(t, target, []byte("@lkal \"0.1\"\nsibling = \"keep\"\n"))
	writeFile(t, declaration, []byte("@lkal \"0.1\"\nx = 1\n"))
	physical, err := provider.InspectPhysicalIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg := host.Config{Version: 1, Actor: "host-actor", StoreID: "store", StoreDirectory: store, LockDirectory: filepath.Join(dir, "locks"), Resources: []host.ResourceConfig{{ID: "app", ManagementRoot: dir, TargetPath: target, DeviceID: physical.DeviceID, DeclarationFile: declaration, Scope: []host.RuleConfig{{Path: "/", Match: "root", Permissions: []string{"Set", "Delete", "Own", "Resolve"}}}}}}
	path := filepath.Join(dir, "host.json")
	writeConfig(t, path, cfg)
	return cfg, path, target, declaration
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, path string, cfg host.Config) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, raw)
}

func invoke(t *testing.T, config, input string, args ...string) (int, []byte, string) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestCommandProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "KISAGE_TEST_CHILD=1", "KISAGE_HOST_CONFIG="+config)
	command.Stdin = strings.NewReader(input)
	var out, stderr bytes.Buffer
	command.Stdout, command.Stderr = &out, &stderr
	err := command.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return code, out.Bytes(), stderr.String()
}

// #1, #2, #12, #14, #16, #29, #36-38: process restarts must preserve host-bound
// ownership, snapshot digests, sibling values and explicit resolution semantics.
func TestExecutableSixCommandsAndMCPAcrossProcesses(t *testing.T) {
	cfg, config, target, declaration := fixture(t)
	if code, out, stderr := invoke(t, config, "", "--version"); code != 0 || !bytes.Contains(out, []byte("kisage version ")) {
		t.Fatalf("--version: %d %s %s", code, out, stderr)
	}
	if code, _, stderr := invoke(t, config, "", "plan", declaration, "-json"); code != cli.ExitCodeError {
		t.Fatalf("missing store must fail, got %d: %s", code, stderr)
	}
	if code, _, stderr := invoke(t, config, "", "init-store"); code != 0 {
		t.Fatalf("init-store: %d %s", code, stderr)
	}
	if code, _, _ := invoke(t, config, "", "init-store"); code != cli.ExitCodeError {
		t.Fatal("initialization overwrote an existing store")
	}
	if code, out, stderr := invoke(t, config, "", "plan", declaration, "-detailed-exitcode", "-json"); code != cli.ExitCodePlanDiff || !bytes.Contains(out, []byte("Planned")) {
		t.Fatalf("plan: %d %s %s", code, out, stderr)
	}
	if code, out, stderr := invoke(t, config, "", "apply", declaration, "--actor", "spoofed", "-json"); code != 0 || !bytes.Contains(out, []byte("Converged")) {
		t.Fatalf("apply: %d %s %s", code, out, stderr)
	}
	repo, err := protocol.OpenFileStoreRepository(cfg.StoreDirectory)
	if err != nil {
		t.Fatal(err)
	}
	store, err := repo.LoadStore()
	_ = repo.Close()
	if err != nil {
		t.Fatal(err)
	}
	entry, exists := store.Get("app", model.MustParsePath("/x"))
	if !exists || entry.Manager != cfg.Actor || entry.Origin != ownership.OriginCreated {
		t.Fatalf("ownership did not survive process restart: %+v", entry)
	}
	writeFile(t, declaration, []byte("@lkal \"0.1\"\n"))
	if code, _, stderr := invoke(t, config, "", "prune", declaration, "-json"); code != 0 {
		t.Fatalf("prune: %d %s", code, stderr)
	}
	raw, err := os.ReadFile(target)
	if err != nil || !bytes.Contains(raw, []byte("sibling")) || bytes.Contains(raw, []byte("x =")) {
		t.Fatalf("prune lost sibling or retained retired Created: %s (%v)", raw, err)
	}
	writeFile(t, target, []byte("@lkal \"0.1\"\nsibling = \"keep\"\nx = 2\n"))
	writeFile(t, declaration, []byte("@lkal \"0.1\"\nx = 1\n"))
	_, raw, _ = invoke(t, config, "", "explain", declaration, "/x", "-json")
	var explanation cli.CommandDocument
	if err := json.Unmarshal(raw, &explanation); err != nil || explanation.Explanation == nil {
		t.Fatalf("explain: %s (%v)", raw, err)
	}
	if code, out, stderr := invoke(t, config, "", "adopt", declaration, "/x", "--expect", explanation.Explanation.ConditionToken, "--owner-revision", "0", "-json"); code != cli.ExitCodeError {
		// /x was released by prune, so its tombstone revision is nonzero.
		t.Fatalf("stale ownership revision accepted: %d %s %s", code, out, stderr)
	}
	args := []string{"adopt", declaration, "/x", "--expect", explanation.Explanation.ConditionToken, "--owner-revision", jsonNumber(explanation.Explanation.OwnerRevision), "-json"}
	if code, out, stderr := invoke(t, config, "", args...); code != 0 {
		t.Fatalf("adopt: %d %s %s", code, out, stderr)
	}
	_, raw, _ = invoke(t, config, "", "explain", declaration, "/x", "-json")
	if err := json.Unmarshal(raw, &explanation); err != nil {
		t.Fatal(err)
	}
	args[0] = "release"
	args[4] = explanation.Explanation.ConditionToken
	args[6] = jsonNumber(explanation.Explanation.OwnerRevision)
	if code, out, stderr := invoke(t, config, "", args...); code != 0 || !bytes.Contains(out, []byte(`"remanageOnNextApply": true`)) {
		t.Fatalf("release: %d %s %s", code, out, stderr)
	}
	raw, _ = os.ReadFile(target)
	if !bytes.Contains(raw, []byte("x = 2")) {
		t.Fatalf("adopt/release rewrote environment: %s", raw)
	}
	rpc := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	code, out, stderr := invoke(t, config, rpc, "mcp")
	if code != 0 || !bytes.Contains(out, []byte("kisage_release")) || bytes.Contains(out, []byte("init-store")) || len(bytes.Split(bytes.TrimSpace(out), []byte("\n"))) != 2 {
		t.Fatalf("MCP process: %d %s %s", code, out, stderr)
	}
}

func jsonNumber(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestHostPolicyFailsClosed(t *testing.T) {
	cfg, config, _, declaration := fixture(t)
	for _, scenario := range []string{"world-readable", "symlink", "unknown-permission", "wrong-device", "unknown-field", "duplicate-resource"} {
		t.Run(scenario, func(t *testing.T) {
			copyConfig := filepath.Join(filepath.Dir(config), scenario+".json")
			writeConfig(t, copyConfig, cfg)
			switch scenario {
			case "world-readable":
				if err := os.Chmod(copyConfig, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				copyConfig += ".link"
				if err := os.Symlink(config, copyConfig); err != nil {
					t.Fatal(err)
				}
			case "unknown-permission":
				raw, _ := os.ReadFile(copyConfig)
				writeFile(t, copyConfig, bytes.ReplaceAll(raw, []byte(`"Set"`), []byte(`"Force"`)))
			case "wrong-device":
				bad := cfg
				bad.Resources = append([]host.ResourceConfig(nil), cfg.Resources...)
				bad.Resources[0].DeviceID++
				writeConfig(t, copyConfig, bad)
			case "unknown-field":
				raw, _ := os.ReadFile(copyConfig)
				raw = append([]byte(`{"grantEverything":true,`), raw[1:]...)
				writeFile(t, copyConfig, raw)
			case "duplicate-resource":
				bad := cfg
				bad.Resources = append(append([]host.ResourceConfig(nil), cfg.Resources...), cfg.Resources[0])
				writeConfig(t, copyConfig, bad)
			}
			if code, out, _ := invoke(t, copyConfig, "", "apply", declaration, "-json"); code == 0 || !json.Valid(out) {
				t.Fatalf("unsafe host config accepted or error lost: code=%d out=%s", code, out)
			}
		})
	}
}

func TestMissingHostConfigReportsPathAndPreservesExitCodes(t *testing.T) {
	_, realConfig, _, declaration := fixture(t)
	missingParentCfg := filepath.Join(t.TempDir(), "missing", "host.json")

	existingDir := t.TempDir()
	if err := os.Chmod(existingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	missingFileCfg := filepath.Join(existingDir, "missing-host.json")

	symlinkCfg := filepath.Join(existingDir, "symlink-host.json")
	if err := os.Symlink(realConfig, symlinkCfg); err != nil {
		t.Fatal(err)
	}

	dirCfg := filepath.Join(existingDir, "dir-host")
	if err := os.Mkdir(dirCfg, 0o700); err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		name           string
		cfgPath        string
		errSubstr      string
		diagPrefixed   bool
		wantExec       string
		wantPlanExit   int
		wantDetailExit int
		wantApplyExit  int
		wantMCPExit    int
	}{
		{
			name:           "missing-parent-dir",
			cfgPath:        missingParentCfg,
			errSubstr:      "no such file or directory",
			diagPrefixed:   false,
			wantExec:       "Failed",
			wantPlanExit:   cli.ExitCodeError,
			wantDetailExit: cli.ExitCodeError,
			wantApplyExit:  cli.ExitCodeError,
			wantMCPExit:    cli.ExitCodeError,
		},
		{
			name:           "missing-file-in-existing-dir",
			cfgPath:        missingFileCfg,
			errSubstr:      "no such file or directory",
			diagPrefixed:   true,
			wantExec:       "Blocked",
			wantPlanExit:   cli.ExitCodeUsage,
			wantDetailExit: cli.ExitCodeError,
			wantApplyExit:  cli.ExitCodeUsage,
			wantMCPExit:    cli.ExitCodeError,
		},
		{
			name:           "symlink-to-regular-file",
			cfgPath:        symlinkCfg,
			errSubstr:      "symbolic link",
			diagPrefixed:   true,
			wantExec:       "Blocked",
			wantPlanExit:   cli.ExitCodeUsage,
			wantDetailExit: cli.ExitCodeError,
			wantApplyExit:  cli.ExitCodeUsage,
			wantMCPExit:    cli.ExitCodeError,
		},
		{
			name:           "directory",
			cfgPath:        dirCfg,
			errSubstr:      "special file or directory",
			diagPrefixed:   true,
			wantExec:       "Blocked",
			wantPlanExit:   cli.ExitCodeUsage,
			wantDetailExit: cli.ExitCodeError,
			wantApplyExit:  cli.ExitCodeUsage,
			wantMCPExit:    cli.ExitCodeError,
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			invocations := []struct {
				args     []string
				wantExit int
				isJSON   bool
			}{
				{args: []string{"plan", declaration}, wantExit: row.wantPlanExit},
				{args: []string{"plan", declaration, "-detailed-exitcode"}, wantExit: row.wantDetailExit},
				{args: []string{"plan", declaration, "-json"}, wantExit: row.wantPlanExit, isJSON: true},
				{args: []string{"plan", declaration, "-detailed-exitcode", "-json"}, wantExit: row.wantDetailExit, isJSON: true},
				{args: []string{"apply", declaration}, wantExit: row.wantApplyExit},
				{args: []string{"apply", declaration, "-json"}, wantExit: row.wantApplyExit, isJSON: true},
			}
			for _, inv := range invocations {
				var stdout, stderr bytes.Buffer
				code := run(inv.args, strings.NewReader(""), &stdout, &stderr, row.cfgPath)
				if code != inv.wantExit {
					t.Fatalf("args=%v: expected exit code %d, got %d (stderr=%q)", inv.args, inv.wantExit, code, stderr.String())
				}
				errLine := strings.TrimSpace(stderr.String())
				if row.diagPrefixed && !strings.HasPrefix(errLine, string(model.CodeInvalidInput)+": ") {
					t.Fatalf("args=%v: expected K101 prefix in stderr %q", inv.args, errLine)
				}
				if !row.diagPrefixed && strings.HasPrefix(errLine, string(model.CodeInvalidInput)+": ") {
					t.Fatalf("args=%v: unexpected K101 prefix in stderr %q", inv.args, errLine)
				}
				reason := strings.TrimPrefix(errLine, string(model.CodeInvalidInput)+": ")
				for _, want := range []string{"host configuration", row.cfgPath, row.errSubstr, "KISAGE_HOST_CONFIG", "/etc/kisage/host.json"} {
					if !strings.Contains(errLine, want) {
						t.Fatalf("args=%v: stderr %q missing %q", inv.args, errLine, want)
					}
					if !strings.Contains(reason, want) {
						t.Fatalf("args=%v: reason %q missing %q", inv.args, reason, want)
					}
				}
				if inv.isJSON {
					if inv.args[0] == "plan" {
						var doc cli.PlanDocument
						if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
							t.Fatalf("unmarshal plan doc: %v\n%s", err, stdout.String())
						}
						if doc.Summary.Execution != row.wantExec || doc.Recovery.Status != "NoOp" || doc.Plans.Apply.Summary.Execution != row.wantExec || doc.Plans.Prune.Summary.Execution != row.wantExec {
							t.Fatalf("unexpected plan skeleton or summary.execution: %+v", doc)
						}
						if len(doc.Plans.Apply.Summary.Diagnostics) != 1 || doc.Plans.Apply.Summary.Diagnostics[0].Code != string(model.CodeInvalidInput) || doc.Plans.Apply.Summary.Diagnostics[0].Reason != reason {
							t.Fatalf("unexpected plan diagnostics: %+v (want reason %q)", doc.Plans.Apply.Summary.Diagnostics, reason)
						}
					} else {
						var doc cli.CommandDocument
						if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
							t.Fatalf("unmarshal command doc: %v\n%s", err, stdout.String())
						}
						if doc.Summary.Execution != row.wantExec || doc.Recovery.Status != "NoOp" {
							t.Fatalf("unexpected command skeleton or summary.execution: %+v", doc)
						}
						if len(doc.Summary.Diagnostics) != 1 || doc.Summary.Diagnostics[0].Code != string(model.CodeInvalidInput) || doc.Summary.Diagnostics[0].Reason != reason {
							t.Fatalf("unexpected command diagnostics: %+v (want reason %q)", doc.Summary.Diagnostics, reason)
						}
					}
				} else {
					wants := []string{"recovery:", "summary: execution=" + row.wantExec, "K101: " + reason}
					if inv.args[0] == "plan" {
						wants = append(wants, "plans.apply:", "plans.prune:")
					}
					for _, want := range wants {
						if !strings.Contains(stdout.String(), want) {
							t.Fatalf("args=%v: text output %q missing %q", inv.args, stdout.String(), want)
						}
					}
				}
			}

			var mcpOut, mcpErr bytes.Buffer
			if code := run([]string{"mcp"}, strings.NewReader(""), &mcpOut, &mcpErr, row.cfgPath); code != row.wantMCPExit {
				t.Fatalf("mcp exit code: got %d, want %d", code, row.wantMCPExit)
			}
			for _, want := range []string{"host configuration", row.cfgPath, row.errSubstr, "KISAGE_HOST_CONFIG", "/etc/kisage/host.json"} {
				if !strings.Contains(mcpErr.String(), want) {
					t.Fatalf("mcp stderr %q missing %q", mcpErr.String(), want)
				}
			}
		})
	}

	if code, _, stderr := invoke(t, missingParentCfg, "", "plan", declaration, "-detailed-exitcode"); code != cli.ExitCodeError || !strings.Contains(stderr, missingParentCfg) || !strings.Contains(stderr, "KISAGE_HOST_CONFIG") {
		t.Fatalf("subprocess missing KISAGE_HOST_CONFIG: code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Lstat("/etc/kisage/host.json"); os.IsNotExist(err) {
		if code, _, stderr := invoke(t, "", "", "plan", declaration, "-detailed-exitcode"); code != cli.ExitCodeError || !strings.Contains(stderr, "/etc/kisage/host.json") || !strings.Contains(stderr, "KISAGE_HOST_CONFIG") {
			t.Fatalf("subprocess default config missing: code=%d stderr=%q", code, stderr)
		}
	}
}

func TestMCPDualEraStdioEndToEnd(t *testing.T) {
	_, cfgPath, _, declaration := fixture(t)

	var initOut, initErr bytes.Buffer
	if code := run([]string{"init-store"}, strings.NewReader(""), &initOut, &initErr, cfgPath); code != cli.ExitCodeOK {
		t.Fatalf("init-store failed: code=%d stderr=%s", code, initErr.String())
	}

	declJSON, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}

	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"_meta":{"progressToken":1},"name":"kisage_plan","arguments":{"file":` + string(declJSON) + `}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"other-actor","version":"9"}},"name":"kisage_plan","arguments":{"file":` + string(declJSON) + `}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}},"name":"kisage_plan","arguments":{"file":` + string(declJSON) + `}}}`,
	}
	stdin := strings.NewReader(strings.Join(frames, "\n") + "\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"mcp"}, stdin, &stdout, &stderr, cfgPath)
	if code != cli.ExitCodeOK {
		t.Fatalf("mcp exited with %d: stderr=%s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != len(frames) {
		t.Fatalf("expected %d response lines, got %d:\n%s", len(frames), len(lines), stdout.String())
	}

	decodeLine := func(idx int) map[string]any {
		t.Helper()
		var msg map[string]any
		if err := json.Unmarshal([]byte(lines[idx]), &msg); err != nil {
			t.Fatalf("line %d invalid JSON: %v (%s)", idx+1, err, lines[idx])
		}
		return msg
	}

	// a. server/discover
	respA := decodeLine(0)
	if respA["error"] != nil {
		t.Fatalf("line 1 unexpected error: %v", respA["error"])
	}
	resA, ok := respA["result"].(map[string]any)
	if !ok {
		t.Fatalf("line 1 expected result object, got %T", respA["result"])
	}
	if resA["resultType"] != "complete" {
		t.Fatalf("line 1 resultType = %v, want complete", resA["resultType"])
	}
	versions, ok := resA["supportedVersions"].([]any)
	if !ok {
		t.Fatalf("line 1 supportedVersions = %T, want array", resA["supportedVersions"])
	}
	found2026 := false
	for _, v := range versions {
		if v == "2026-07-28" {
			found2026 = true
			break
		}
	}
	if !found2026 {
		t.Fatalf("line 1 supportedVersions %v does not contain 2026-07-28", versions)
	}

	// b. legacy initialize
	respB := decodeLine(1)
	if respB["error"] != nil {
		t.Fatalf("line 2 unexpected error: %v", respB["error"])
	}
	resB, ok := respB["result"].(map[string]any)
	if !ok {
		t.Fatalf("line 2 expected result object, got %T", respB["result"])
	}
	if resB["protocolVersion"] != "2025-11-25" {
		t.Fatalf("line 2 protocolVersion = %v, want 2025-11-25", resB["protocolVersion"])
	}

	// c. legacy tools/call kisage_plan
	respC := decodeLine(2)
	if respC["error"] != nil {
		t.Fatalf("line 3 unexpected error: %v", respC["error"])
	}
	resC, ok := respC["result"].(map[string]any)
	if !ok {
		t.Fatalf("line 3 expected result object, got %T", respC["result"])
	}
	if resC["isError"] != false {
		t.Fatalf("line 3 isError = %v, want false (result=%v)", resC["isError"], resC)
	}

	// d. modern tools/call kisage_plan
	respD := decodeLine(3)
	if respD["error"] != nil {
		t.Fatalf("line 4 unexpected error: %v", respD["error"])
	}
	resD, ok := respD["result"].(map[string]any)
	if !ok {
		t.Fatalf("line 4 expected result object, got %T", respD["result"])
	}
	if resD["resultType"] != "complete" {
		t.Fatalf("line 4 resultType = %v, want complete", resD["resultType"])
	}
	if !reflect.DeepEqual(resD["structuredContent"], resC["structuredContent"]) {
		t.Fatalf("line 4 structuredContent mismatch:\nmodern: %#v\nlegacy: %#v", resD["structuredContent"], resC["structuredContent"])
	}

	// e. unsupported protocolVersion -32022
	respE := decodeLine(4)
	errE, ok := respE["error"].(map[string]any)
	if !ok {
		t.Fatalf("line 5 expected JSON-RPC error object, got %v", respE)
	}
	if errE["code"] != float64(-32022) {
		t.Fatalf("line 5 error.code = %v, want -32022", errE["code"])
	}
	dataE, ok := errE["data"].(map[string]any)
	if !ok {
		t.Fatalf("line 5 expected error.data object, got %T", errE["data"])
	}
	if dataE["requested"] != "1900-01-01" {
		t.Fatalf("line 5 error.data.requested = %v, want 1900-01-01", dataE["requested"])
	}
}
