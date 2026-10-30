package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
