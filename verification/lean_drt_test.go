package verification_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/scope"
	"github.com/Kaikei-e/kisage/verification"
)

// This projection compares every field represented by the Lean model. Go-only
// revisions, batch IDs, condition tokens and diagnostics have separate conformance checks.
type drtSettled struct {
	Origin      string `json:"origin"`
	LastApplied *int64 `json:"lastApplied"`
}
type drtPending struct {
	Op       string      `json:"op"`
	Expected *int64      `json:"expected"`
	Target   *int64      `json:"target"`
	Previous *drtSettled `json:"previous"`
}
type drtOwner struct {
	Manager     int         `json:"manager"`
	Origin      string      `json:"origin"`
	LastApplied *int64      `json:"lastApplied"`
	Pending     *drtPending `json:"pending"`
}
type drtEnv struct {
	Path     int    `json:"path"`
	Op       string `json:"op"`
	Expected *int64 `json:"expected"`
	Target   *int64 `json:"target"`
}
type drtMeta struct {
	Path   int       `json:"path"`
	Action string    `json:"action"`
	After  *drtOwner `json:"after"`
}
type drtUpdate struct {
	Path           int       `json:"path"`
	CreatesPending bool      `json:"createsPending"`
	After          *drtOwner `json:"after"`
}
type drtPerms struct {
	Set     bool `json:"set"`
	Delete  bool `json:"delete"`
	Own     bool `json:"own"`
	Resolve bool `json:"resolve"`
}
type leanDRTPayload struct {
	RecIsCandidate    bool       `json:"recIsCandidate"`
	RequiredPerms     drtPerms   `json:"requiredPerms"`
	RecoveryKind      string     `json:"recoveryKind"`
	VirtualOwner      *drtOwner  `json:"virtualOwner"`
	EnvOp             *drtEnv    `json:"envOp"`
	MetadataCandidate *drtMeta   `json:"metadataCandidate"`
	OwnershipUpdate   *drtUpdate `json:"ownershipUpdate"`
}

type leanDRTCase struct {
	leanDRTPayload
	Mode          string `json:"mode"`
	Decl          string `json:"decl"`
	Obs           string `json:"obs"`
	Anc           string `json:"anc"`
	Owner         string `json:"owner"`
	Perms         string `json:"perms"`
	Overlap       string `json:"overlap"`
	StateClass    string `json:"stateClass"`
	Selection     string `json:"selection"`
	Decision      string `json:"decision"`
	Action        string `json:"action"`
	HasEnvOp      bool   `json:"hasEnvOp"`
	HasMetaCand   bool   `json:"hasMetaCand"`
	RecAuthorized bool   `json:"recAuthorized"`
	RecUnresolved bool   `json:"recUnresolved"`
}

var forbiddenLeanIdentifiers = []string{
	"sorry",
	"sorryAx",
	"admit",
	"axiom",
	"unsafe",
	"implemented_by",
	"extern",
}

// TestLean4NoSorryAxioms verifies that all Lean 4 files in Kisage/ exist and contain
// zero occurrences of `sorry`, `sorryAx`, `admit`, `axiom`, `unsafe`, `implemented_by`,
// or `extern` in executable/proof code (ADR 0005 §3).
func TestLean4NoSorryAxioms(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "Kisage", "*.lean"))
	if err != nil || len(files) == 0 {
		t.Fatalf("expected Kisage/*.lean files, got %v (err=%v)", files, err)
	}
	files = append(files, filepath.Join("..", "Kisage.lean"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("failed to read %s: %v", f, err)
		}
		if err := checkLeanFileForSorry(string(raw)); err != nil {
			t.Fatalf("lean check failed in %s: %v", f, err)
		}
	}
}

func checkLeanFileForSorry(src string) error {
	code, err := stripLeanCommentsAndStrings(src)
	if err != nil {
		return err
	}
	for _, forbidden := range forbiddenLeanIdentifiers {
		if containsLeanIdentifier(code, forbidden) {
			return fmt.Errorf("found forbidden identifier %q", forbidden)
		}
	}
	return nil
}

func stripLeanComments(src string) (string, error) {
	return stripLeanCommentsAndStrings(src)
}

func stripLeanCommentsAndStrings(src string) (string, error) {
	var out strings.Builder
	out.Grow(len(src))
	depth := 0
	inLineComment := false
	inString := false

	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out.WriteByte('\n')
			}
			continue
		}
		if inString {
			if ch == '\\' {
				if i+1 >= len(src) {
					return "", fmt.Errorf("unclosed string literal after escape at EOF")
				}
				i++
				out.WriteByte(' ')
				continue
			}
			if ch == '"' {
				inString = false
				out.WriteByte('"')
				continue
			}
			if ch == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
			continue
		}
		if depth > 0 {
			if ch == '/' && i+1 < len(src) && src[i+1] == '-' {
				depth++
				i++
				out.WriteByte(' ')
				continue
			}
			if ch == '-' && i+1 < len(src) && src[i+1] == '/' {
				depth--
				i++
				out.WriteByte(' ')
				continue
			}
			if ch == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
			continue
		}

		if ch == '/' && i+1 < len(src) && src[i+1] == '-' {
			depth = 1
			i++
			out.WriteByte(' ')
			continue
		}
		if ch == '-' && i+1 < len(src) && src[i+1] == '/' {
			return "", fmt.Errorf("unmatched block comment terminator -/ at offset %d", i)
		}
		if ch == '-' && i+1 < len(src) && src[i+1] == '-' {
			inLineComment = true
			i++
			out.WriteByte(' ')
			continue
		}
		if ch == '"' {
			inString = true
			out.WriteByte('"')
			continue
		}
		out.WriteByte(ch)
	}
	if inString {
		return "", fmt.Errorf("unclosed string literal at EOF")
	}
	if depth > 0 {
		return "", fmt.Errorf("unclosed block comment (depth=%d) at EOF", depth)
	}
	return out.String(), nil
}

func isLeanIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '\''
}

func containsLeanIdentifier(code, ident string) bool {
	if ident == "" {
		return false
	}
	offset := 0
	for {
		idx := strings.Index(code[offset:], ident)
		if idx < 0 {
			return false
		}
		pos := offset + idx
		end := pos + len(ident)
		leftOK := pos == 0 || !isLeanIdentChar(code[pos-1])
		rightOK := end == len(code) || !isLeanIdentChar(code[end])
		if leftOK && rightOK {
			return true
		}
		offset = pos + 1
	}
}

// TestLean4GoDifferentialTesting executes the compiled Lean 4 `kisage-drt` binary across
// all 1,728 valid combinations of (Mode, Declared, Observed, AncestorStatus, OwnerState, ScopePerms, OverlapKind)
// and verifies 100% agreement between the Lean 4 formal model and the Go `plan.Plan` reference implementation (ADR 0005 §3–4).
func TestLean4GoDifferentialTesting(t *testing.T) {
	out := leanDRTOutput(t)

	targetPath := model.MustParsePath("/app/leaf")
	ancPath := model.MustParsePath("/app")
	childPath := model.MustParsePath("/app/leaf/sub")

	scanner := bufio.NewScanner(bytes.NewReader(out))
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var tc leanDRTCase
		if err := json.Unmarshal([]byte(line), &tc); err != nil {
			t.Fatalf("invalid JSON line from kisage-drt: %v (%s)", err, line)
		}
		count++

		in, mode := buildGoInputFromLeanCase(t, tc, targetPath, ancPath, childPath)
		res, err := plan.Plan(in, mode)
		if err != nil {
			t.Fatalf("case %d (%+v): Go plan.Plan failed: %v", count, tc, err)
		}
		if err := verification.VerifyAll11PlanTheorems(in); err != nil {
			t.Fatalf("case %d (%+v): Theorem check failed: %v", count, tc, err)
		}

		if res.Recovery.HasCandidates() != tc.RecAuthorized {
			t.Fatalf("case %d (%+v): RecAuthorized mismatch: Lean=%v Go=%v", count, tc, tc.RecAuthorized, res.Recovery.HasCandidates())
		}
		if res.Recovery.HasUnresolved() != tc.RecUnresolved {
			t.Fatalf("case %d (%+v): RecUnresolved mismatch: Lean=%v Go=%v", count, tc, tc.RecUnresolved, res.Recovery.HasUnresolved())
		}

		if err := compareDRTPayload(t, tc.leanDRTPayload, res, targetPath); err != nil {
			t.Fatalf("case %d (%+v): %v", count, tc, err)
		}

		pr, found := findPathResultForDRT(res.Gate.PathResults, targetPath)
		if tc.StateClass == "OutsideDomain" {
			if found {
				t.Fatalf("case %d (%+v): expected targetPath %s to be outside M_A(d, σ_virtual), got %+v", count, tc, targetPath, pr)
			}
			if tc.Selection != "NoOp" || tc.Decision != "NoOp" || tc.Action != "" || tc.HasEnvOp || tc.HasMetaCand {
				t.Fatalf("case %d (%+v): unexpected OutsideDomain outputs", count, tc)
			}
			continue
		}
		if !found {
			t.Fatalf("case %d (%+v): targetPath %s not found in Go PathResults", count, tc, targetPath)
		}

		ap, foundAuth := findAuthorizedForDRT(res.Authorized, targetPath)
		if !foundAuth {
			t.Fatalf("case %d (%+v): targetPath %s not found in Go Authorized list", count, tc, targetPath)
		}

		gotDecision := ap.Decision.String()
		if res.Recovery.HasUnresolved() && hasUnresolvedForTarget(res.Recovery, targetPath) {
			gotDecision = plan.DecisionBlocker.String()
		}

		if pr.StateClass.String() != tc.StateClass {
			t.Fatalf("case %d (%+v): StateClass mismatch: Lean=%s Go=%s", count, tc, tc.StateClass, pr.StateClass)
		}
		if pr.Selection.String() != tc.Selection {
			t.Fatalf("case %d (%+v): Selection mismatch: Lean=%s Go=%s", count, tc, tc.Selection, pr.Selection)
		}
		if gotDecision != tc.Decision {
			t.Fatalf("case %d (%+v): Decision mismatch: Lean=%s Go=%s", count, tc, tc.Decision, gotDecision)
		}
		var goAction string
		if pr.Action != 0 {
			goAction = pr.Action.String()
		}
		if goAction != tc.Action {
			t.Fatalf("case %d (%+v): Action mismatch: Lean=%q Go=%q", count, tc, tc.Action, goAction)
		}
		if hasEnvOpForDRT(res.Gate.Changes, targetPath) != tc.HasEnvOp {
			t.Fatalf("case %d (%+v): HasEnvOp mismatch: Lean=%v Go=%v", count, tc, tc.HasEnvOp, hasEnvOpForDRT(res.Gate.Changes, targetPath))
		}
		if hasMetaCandForDRT(res.Gate.MetadataCandidates, targetPath) != tc.HasMetaCand {
			t.Fatalf("case %d (%+v): HasMetaCand mismatch: Lean=%v Go=%v", count, tc, tc.HasMetaCand, hasMetaCandForDRT(res.Gate.MetadataCandidates, targetPath))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1728 {
		t.Fatalf("expected 1728 DRT cases from Lean 4 model, got %d", count)
	}
}

func buildGoInputFromLeanCase(
	t *testing.T,
	tc leanDRTCase,
	targetPath, ancPath, childPath model.Path,
) (plan.Input, plan.Mode) {
	t.Helper()
	mode := plan.ModeApply
	if tc.Mode == "Prune" {
		mode = plan.ModePrune
	}

	var declSrc string
	if tc.Decl == "declared_10" {
		declSrc = "app {\n  leaf = 10\n}\n"
	} else if tc.Overlap == "retired_created" {
		declSrc = "app {\n  leaf {\n    sub = 99\n  }\n}\n"
	}
	decl := mustDecl(t, declSrc)

	snap := observation.NewSnapshot()
	switch tc.Anc {
	case "clear":
		snap.Set(ancPath, observation.PresentRecord())
	case "protected":
		snap.Set(ancPath, observation.MustPresentLeaf(lkal.String("blocking-leaf-ancestor")))
	case "unknown":
		snap.Set(ancPath, observation.UnknownObserved("ancestor unknown"))
	}

	switch tc.Obs {
	case "absent":
		snap.Set(targetPath, observation.AbsentObserved())
	case "leaf_10":
		snap.Set(targetPath, observation.MustPresentLeaf(lkal.Int(10)))
	case "leaf_20":
		snap.Set(targetPath, observation.MustPresentLeaf(lkal.Int(20)))
	case "record":
		snap.Set(targetPath, observation.PresentRecord())
	case "opaque":
		snap.Set(targetPath, observation.PresentOpaque("unrepresentable value"))
	case "unknown":
		snap.Set(targetPath, observation.UnknownObserved("target unknown"))
	}
	snap.Set(childPath, observation.AbsentObserved())

	store := ownership.NewStore()
	var err error
	switch tc.Owner {
	case "self_created_10":
		e, _ := ownership.NewSettledCreated(actorA, lkal.Int(10), 1)
		store, err = store.Put(testResID, targetPath, e)
	case "self_modified_10":
		e, _ := ownership.NewSettledModified(actorA, lkal.Int(10), 1)
		store, err = store.Put(testResID, targetPath, e)
	case "foreign_created_10":
		e, _ := ownership.NewSettledCreated(actorB, lkal.Int(10), 1)
		store, err = store.Put(testResID, targetPath, e)
	case "self_pending_create_10":
		e, _ := ownership.NewPendingCreate(actorA, "b-drt", lkal.Int(10), 1)
		store, err = store.Put(testResID, targetPath, e)
	case "self_pending_update_created_20":
		base, _ := ownership.NewSettledCreated(actorA, lkal.Int(10), 1)
		e, _ := ownership.BeginSetUpdate(base, "b-drt", lkal.Int(20), 2)
		store, err = store.Put(testResID, targetPath, e)
	case "self_pending_delete_10":
		base, _ := ownership.NewSettledCreated(actorA, lkal.Int(10), 1)
		e, _ := ownership.BeginDelete(base, "b-drt", 2)
		store, err = store.Put(testResID, targetPath, e)
	}
	if err != nil {
		t.Fatalf("failed to put owner %s: %v", tc.Owner, err)
	}

	switch tc.Overlap {
	case "retired_created":
		if tc.Decl == "declared_10" {
			e, _ := ownership.NewSettledCreated(actorA, lkal.Int(99), 1)
			store, err = store.Put(testResID, childPath, e)
		}
	case "retired_modified":
		if tc.Decl == "declared_10" {
			e, _ := ownership.NewSettledModified(actorA, lkal.Int(99), 1)
			store, err = store.Put(testResID, childPath, e)
		}
	case "foreign_conflict":
		if tc.Decl == "declared_10" {
			e, _ := ownership.NewSettledCreated(actorB, lkal.Int(99), 1)
			store, err = store.Put(testResID, childPath, e)
		}
	}
	if err != nil {
		t.Fatalf("failed to put overlap %s: %v", tc.Overlap, err)
	}

	var sc scope.Scope
	switch tc.Perms {
	case "none":
		r, err := scope.NewSubtreeRule(model.MustParsePath("/unrelated"), scope.NewPermissionSet(scope.PermOwn))
		if err != nil {
			t.Fatal(err)
		}
		sc = scope.MustNew(r)
	case "own":
		r, err := scope.NewRootRule(scope.NewPermissionSet(scope.PermOwn))
		if err != nil {
			t.Fatal(err)
		}
		sc = scope.MustNew(r)
	case "regular":
		r, err := scope.NewRootRule(scope.PermRegular)
		if err != nil {
			t.Fatal(err)
		}
		sc = scope.MustNew(r)
	}

	return plan.Input{
		Resource:    testResID,
		Actor:       actorA,
		Declaration: decl,
		Snapshot:    snap,
		Store:       store,
		Scope:       sc,
	}, mode
}

func findPathResultForDRT(results []plan.PathResult, target model.Path) (plan.PathResult, bool) {
	for _, pr := range results {
		if pr.Path.Equal(target) {
			return pr, true
		}
	}
	return plan.PathResult{}, false
}

func findAuthorizedForDRT(results []plan.AuthorizedPath, target model.Path) (plan.AuthorizedPath, bool) {
	for _, ap := range results {
		if ap.Selected.Classified.Path.Equal(target) {
			return ap, true
		}
	}
	return plan.AuthorizedPath{}, false
}

func hasUnresolvedForTarget(rec *plan.RecoveryReport, target model.Path) bool {
	if rec == nil {
		return false
	}
	for _, item := range rec.Unresolved {
		if item.Path.Equal(target) {
			return true
		}
	}
	return false
}

func hasEnvOpForDRT(ops []plan.EnvironmentOp, target model.Path) bool {
	for _, op := range ops {
		if op.Path.Equal(target) {
			return true
		}
	}
	return false
}

func hasMetaCandForDRT(cands []plan.MetadataCandidate, target model.Path) bool {
	for _, c := range cands {
		if c.Path.Equal(target) {
			return true
		}
	}
	return false
}

var drtBuildOnce sync.Once
var drtBuildOutput []byte
var drtBuildErr error

// Build current sources, including proofs, before executing DRT. A missing or stale
// binary must never silently turn the verification gate into a passing skipped test.
func leanDRTOutput(t *testing.T) []byte {
	t.Helper()
	drtBuildOnce.Do(func() {
		lakeBin := "lake"
		var extraEnv []string
		if _, err := exec.LookPath(lakeBin); err != nil {
			if home, hErr := os.UserHomeDir(); hErr == nil {
				candidate := filepath.Join(home, ".elan", "bin", "lake")
				if _, sErr := os.Stat(candidate); sErr == nil {
					lakeBin = candidate
					extraEnv = append(os.Environ(), "PATH="+filepath.Join(home, ".elan", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
				}
			}
		}
		build := exec.Command(lakeBin, "build")
		build.Dir = ".."
		if len(extraEnv) > 0 {
			build.Env = extraEnv
		}
		if out, err := build.CombinedOutput(); err != nil {
			drtBuildErr = fmt.Errorf("lake build failed (install the pinned Lean toolchain): %w\n%s", err, out)
			return
		}
		drtBuildOutput, drtBuildErr = exec.Command(filepath.Join("..", ".lake", "build", "bin", "kisage-drt")).Output()
	})
	if drtBuildErr != nil {
		t.Fatal(drtBuildErr)
	}
	return drtBuildOutput
}

func drtValue(t *testing.T, value lkal.Value) *int64 {
	t.Helper()
	if value == nil {
		return nil
	}
	v, ok := value.(lkal.Int)
	if !ok {
		t.Fatalf("unexpected DRT value type %T", value)
	}
	n := int64(v)
	return &n
}

func drtExpected(t *testing.T, obs observation.Observed) *int64 {
	t.Helper()
	if obs.IsAbsent() {
		return nil
	}
	v, ok := obs.LeafValue()
	if !ok {
		t.Fatalf("unexpected DRT expectation %v", obs)
	}
	return drtValue(t, v)
}

func normalizeDRTOwner(t *testing.T, entry *ownership.OwnerEntry) *drtOwner {
	t.Helper()
	if entry == nil {
		return nil
	}
	manager := 0
	switch entry.Manager {
	case actorA:
		manager = 1
	case actorB:
		manager = 2
	default:
		t.Fatalf("unexpected DRT manager %q", entry.Manager)
	}
	result := &drtOwner{Manager: manager, Origin: entry.Origin.String(), LastApplied: drtValue(t, entry.LastApplied)}
	if p := entry.Pending; p != nil {
		result.Pending = &drtPending{Op: p.Op.String(), Expected: drtExpected(t, p.Expected), Target: drtValue(t, p.Target)}
		if p.Previous != nil {
			result.Pending.Previous = &drtSettled{Origin: p.Previous.Origin.String(), LastApplied: drtValue(t, p.Previous.LastApplied)}
		}
	}
	return result
}

func goDRTPayload(t *testing.T, res *plan.Result, target model.Path) leanDRTPayload {
	t.Helper()
	result := leanDRTPayload{RecoveryKind: "None"}
	if e, ok := res.Recovery.VirtualStore.Get(res.Resource, target); ok {
		result.VirtualOwner = normalizeDRTOwner(t, &e)
	}
	for _, item := range res.Recovery.Items {
		if item.Path.Equal(target) {
			result.RecoveryKind = item.Kind.String()
			result.RecIsCandidate = item.Authorized || item.Kind == plan.RecoveryPolicyDenied
		}
	}
	if ap, ok := findAuthorizedForDRT(res.Authorized, target); ok {
		perms := ap.Selected.RequiredPerms
		result.RequiredPerms = drtPerms{perms.Contains(scope.PermSet), perms.Contains(scope.PermDelete), perms.Contains(scope.PermOwn), perms.Contains(scope.PermResolve)}
	}
	for _, op := range res.Gate.Changes {
		if op.Path.Equal(target) {
			if result.EnvOp != nil {
				t.Fatal("duplicate DRT environment op")
			}
			result.EnvOp = &drtEnv{Path: 1, Op: op.Op.String(), Expected: drtExpected(t, op.Expected), Target: drtValue(t, op.Target)}
		}
	}
	for _, mc := range res.Gate.MetadataCandidates {
		if mc.Path.Equal(target) {
			if result.MetadataCandidate != nil {
				t.Fatal("duplicate DRT metadata candidate")
			}
			result.MetadataCandidate = &drtMeta{Path: 1, Action: mc.Action.String(), After: normalizeDRTOwner(t, mc.After)}
		}
	}
	for _, u := range res.Gate.OwnershipUpdates {
		if u.Path.Equal(target) {
			if result.OwnershipUpdate != nil {
				t.Fatal("duplicate DRT ownership update")
			}
			result.OwnershipUpdate = &drtUpdate{Path: 1, CreatesPending: !u.IsMetadataOnly, After: normalizeDRTOwner(t, u.After)}
		}
	}
	return result
}

func compareDRTPayload(t *testing.T, want leanDRTPayload, res *plan.Result, target model.Path) error {
	t.Helper()
	got := goDRTPayload(t, res, target)
	if reflect.DeepEqual(want, got) {
		return nil
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	return fmt.Errorf("DRT payload mismatch: Lean=%s Go=%s", a, b)
}

// These mutations reproduce defects that the former boolean-only comparison accepted.
// Each fixture comes from the executable Lean oracle, not a duplicated Go expectation.
func TestLean4DifferentialRejectsPayloadMutations(t *testing.T) {
	var cases []leanDRTCase
	scanner := bufio.NewScanner(bytes.NewReader(leanDRTOutput(t)))
	for scanner.Scan() {
		var tc leanDRTCase
		if err := json.Unmarshal(scanner.Bytes(), &tc); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, tc)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	target := model.MustParsePath("/app/leaf")
	ancestor := model.MustParsePath("/app")
	child := model.MustParsePath("/app/leaf/sub")
	mutations := []struct {
		name       string
		selectCase func(leanDRTCase) bool
		mutate     func(*testing.T, *plan.Result)
	}{
		{"adoption_must_not_grant_created", func(c leanDRTCase) bool {
			return c.MetadataCandidate != nil && c.MetadataCandidate.After != nil
		}, func(t *testing.T, r *plan.Result) {
			r.Gate.OwnershipUpdates[0].After.Origin = ownership.OriginCreated
		}},
		{"set_target", func(c leanDRTCase) bool { return c.EnvOp != nil && c.EnvOp.Op == "Set" },
			func(t *testing.T, r *plan.Result) { r.Gate.Changes[0].Target = lkal.Int(999) }},
		{"set_expectation", func(c leanDRTCase) bool { return c.EnvOp != nil && c.EnvOp.Op == "Set" },
			func(t *testing.T, r *plan.Result) {
				r.Gate.Changes[0].Expected = observation.MustPresentLeaf(lkal.Int(999))
			}},
		{"operation_kind", func(c leanDRTCase) bool { return c.EnvOp != nil && c.EnvOp.Op == "Set" },
			func(t *testing.T, r *plan.Result) { r.Gate.Changes[0].Op = ownership.PendingOpDelete }},
		{"operation_path", func(c leanDRTCase) bool { return c.EnvOp != nil },
			func(t *testing.T, r *plan.Result) { r.Gate.Changes[0].Path = child }},
		{"metadata_payload", func(c leanDRTCase) bool { return c.MetadataCandidate != nil && c.MetadataCandidate.After != nil },
			func(t *testing.T, r *plan.Result) { r.Gate.MetadataCandidates[0].After.LastApplied = lkal.Int(999) }},
		{"pending_separation", func(c leanDRTCase) bool { return c.OwnershipUpdate != nil && c.OwnershipUpdate.CreatesPending },
			func(t *testing.T, r *plan.Result) { r.Gate.OwnershipUpdates[0].IsMetadataOnly = true }},
		{"recovery_value", func(c leanDRTCase) bool { return c.RecAuthorized && c.VirtualOwner != nil },
			func(t *testing.T, r *plan.Result) {
				entry, _ := r.Recovery.VirtualStore.Get(r.Resource, target)
				entry.LastApplied = lkal.Int(999)
				next, err := r.Recovery.VirtualStore.Put(r.Resource, target, entry)
				if err != nil {
					t.Fatal(err)
				}
				r.Recovery.VirtualStore = next
			}},
		{"retained_pending_target", func(c leanDRTCase) bool {
			return c.VirtualOwner != nil && c.VirtualOwner.Pending != nil && c.VirtualOwner.Pending.Op == "Set"
		},
			func(t *testing.T, r *plan.Result) {
				entry, _ := r.Recovery.VirtualStore.Get(r.Resource, target)
				entry.Pending.Target = lkal.Int(999)
				next, err := r.Recovery.VirtualStore.Put(r.Resource, target, entry)
				if err != nil {
					t.Fatal(err)
				}
				r.Recovery.VirtualStore = next
			}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			for _, tc := range cases {
				if !mutation.selectCase(tc) {
					continue
				}
				in, mode := buildGoInputFromLeanCase(t, tc, target, ancestor, child)
				result, err := plan.Plan(in, mode)
				if err != nil {
					t.Fatal(err)
				}
				if err := compareDRTPayload(t, tc.leanDRTPayload, result, target); err != nil {
					t.Fatal(err)
				}
				mutation.mutate(t, result)
				if err := compareDRTPayload(t, tc.leanDRTPayload, result, target); err == nil {
					t.Fatal("Lean/Go DRT accepted corrupted payload")
				}
				return
			}
			t.Fatal("missing Lean fixture for mutation")
		})
	}
}
