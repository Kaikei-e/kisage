package plan_test

import (
	"math"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/observation"
	"github.com/Kaikei-e/kisage/ownership"
	"github.com/Kaikei-e/kisage/plan"
	"github.com/Kaikei-e/kisage/projection"
	"github.com/Kaikei-e/kisage/scope"
)

func TestSecurityRegression_GC03_GC04_TokenBindingAndRevisionOverflow(t *testing.T) {
	p := model.MustParsePath("/app/key")

	// Distinct Opaque values with identical diagnostic reasons must produce distinct ConditionTokens.
	snap1 := observation.ObserveRecord(lkal.Record{
		Fields: []lkal.Field{{
			Key: "app",
			Value: lkal.Record{
				Fields: []lkal.Field{{Key: "key", Value: lkal.List{lkal.Int(1), lkal.String("a")}}},
			},
		}},
	})
	snap2 := observation.ObserveRecord(lkal.Record{
		Fields: []lkal.Field{{
			Key: "app",
			Value: lkal.Record{
				Fields: []lkal.Field{{Key: "key", Value: lkal.List{lkal.Int(1), lkal.String("b")}}},
			},
		}},
	})
	tok1, err := plan.ComputeConditionToken("res-1", p, snap1)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := plan.ComputeConditionToken("res-1", p, snap2)
	if err != nil {
		t.Fatal(err)
	}
	if tok1 == tok2 {
		t.Fatal("expected distinct Opaque values to produce distinct ConditionTokens")
	}

	// PlanAdopt at math.MaxUint64 must reject with K205 instead of wrapping NextRevision to 0.
	decl, err := projection.ParseDeclaration("decl.lkal", []byte("@lkal \"0.1\"\napp {\n  key = \"v2\"\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	snap := observation.NewSnapshot()
	snap.Set(model.MustParsePath("/app"), observation.PresentRecord())
	snap.Set(p, observation.MustPresentLeaf(lkal.String("v1")))

	maxSettled, err := ownership.NewSettledModified("actor-a", lkal.String("old"), math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ownership.NewStore().Put("res-1", p, maxSettled)
	if err != nil {
		t.Fatal(err)
	}
	rule, _ := scope.NewRootRule(scope.PermAll)
	sc := scope.MustNew(rule)
	tokMax, err := plan.ComputeConditionToken("res-1", p, snap)
	if err != nil {
		t.Fatal(err)
	}

	_, err = plan.PlanAdopt(plan.ResolutionRequest{
		Resource:         "res-1",
		Actor:            "actor-a",
		Path:             p,
		ExpectedToken:    tokMax,
		ExpectedRevision: math.MaxUint64,
		Declaration:      decl,
		Snapshot:         snap,
		Store:            store,
		Scope:            sc,
	})
	if err == nil {
		t.Fatal("expected PlanAdopt at math.MaxUint64 to fail on revision overflow")
	}
}
