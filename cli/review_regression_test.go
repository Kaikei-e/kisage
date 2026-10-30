package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/cli"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/protocol"
)

// #14, #31, #41: human previews retain the same actionable information as JSON
// while leaving both virtual recovery and suppressed candidates unpersisted.
func TestTextPlanRetainsPathsRecoveryAndStoppingReasons(t *testing.T) {
	_, decl, repo, _, runner := setupTestCLIEnv(t,
		"@lkal \"0.1\"\napp {\n existing = \"legacy\"\n recovered = \"target\"\n}\n",
		"@lkal \"0.1\"\napp {\n existing = \"desired\"\n recovered = \"target\"\n fresh = \"new\"\n}\n")
	pending, err := ownership.NewPendingCreate(actorA, "old", lkal.String("target"), 1)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ownership.NewStore().Put(testResID, model.MustParsePath("/app/recovered"), pending)
	if err != nil {
		t.Fatal(err)
	}
	if commit := repo.SaveStore(protocol.StageIntentSave, store); commit.Status != protocol.CommitSucceeded {
		t.Fatalf("seed recovery state: %+v", commit)
	}
	var out, stderr bytes.Buffer
	if code := runner.Run([]string{"plan", decl}, &out, &stderr); code != cli.ExitCodeError {
		t.Fatalf("expected Blocked, got code=%d: %s", code, stderr.String())
	}
	for _, want := range []string{"/app/existing", "K206", "adopt", "next:", "/app/fresh", "Suppressed", "desired=\"new\"", "/app/recovered: recovery=CommitSet", "persisted=false", "expected=Absent", "target=\"target\"", "conditional plan"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in text preview:\n%s", want, out.String())
		}
	}
	after, err := repo.LoadStore()
	if err != nil || !reflect.DeepEqual(store, after) {
		t.Fatalf("plan changed persistent recovery state: %v", err)
	}
}

// K102 is an input rejection, but the successfully read bytes still have a
// digest and must yield the normative JSON shape, including detailed exitcode.
func TestDeclarationErrorsPreserveStructuredContext(t *testing.T) {
	target, decl, _, _, runner := setupTestCLIEnv(t, "@lkal \"0.1\"\nx = 1\n", "@lkal \"0.1\"\nx {}\n")
	raw, err := os.ReadFile(decl)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"plan", "apply", "prune", "explain", "adopt", "release"} {
		t.Run(sub, func(t *testing.T) {
			args := []string{sub, decl, "-json"}
			if sub == "explain" || sub == "adopt" || sub == "release" {
				args = append(args, "/x")
			}
			if sub == "adopt" || sub == "release" {
				args = append(args, "--expect", "stale", "--owner-revision", "0")
			}
			var out, stderr bytes.Buffer
			if code := runner.Run(args, &out, &stderr); code != cli.ExitCodeUsage {
				t.Fatalf("expected exit 64, got %d", code)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
				t.Fatalf("no error document: %v: %s", err, out.String())
			}
			var digest, resource string
			_ = json.Unmarshal(envelope["declarationDigest"], &digest)
			_ = json.Unmarshal(envelope["resource"], &resource)
			if digest != cli.ComputeDeclarationDigest(raw) || resource != string(testResID) || !bytes.Contains(out.Bytes(), []byte("K102")) {
				t.Fatalf("lost failure context: %s", out.String())
			}
			if envelope["recovery"] == nil || envelope["summary"] == nil {
				t.Fatalf("incomplete error document: %s", out.String())
			}
			if sub == "plan" {
				out.Reset()
				if code := runner.Run(append(args, "-detailed-exitcode"), &out, &stderr); code != cli.ExitCodeError {
					t.Fatalf("expected detailed error exit 1, got %d", code)
				}
			}
		})
	}
	unchanged, err := os.ReadFile(target)
	if err != nil || string(unchanged) != "@lkal \"0.1\"\nx = 1\n" {
		t.Fatalf("invalid declarations modified the environment: %v", err)
	}
}

func TestUnreadableAndUsageErrorsHaveHonestJSON(t *testing.T) {
	_, decl, _, _, runner := setupTestCLIEnv(t, "@lkal \"0.1\"\n", "@lkal \"0.1\"\n")
	if err := os.Remove(decl); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"apply", decl, "-json"}, {"apply", "-json"}} {
		var out, stderr bytes.Buffer
		if code := runner.Run(args, &out, &stderr); code != cli.ExitCodeUsage {
			t.Fatalf("expected usage error, got %d", code)
		}
		var doc cli.CommandDocument
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.DeclarationDigest != "" || doc.Summary.Convergence != "Unknown" || len(doc.Summary.Diagnostics) == 0 {
			t.Fatalf("unread data was presented as a snapshot: %+v", doc)
		}
	}
}

// #17: O_NOFOLLOW on the declaration's final component alone would accept a
// symbolic-link directory ancestor and read a different protected registration.
func TestDeclarationReaderRejectsSymlinkAncestors(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "desired.lkal")
	if err := os.WriteFile(path, []byte("@lkal \"0.1\"\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ReadDeclarationSnapshot(filepath.Join(alias, "desired.lkal")); err == nil {
		t.Fatal("declaration reader followed a symlink ancestor")
	}
}
