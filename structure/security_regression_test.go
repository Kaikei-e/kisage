package structure_test

import (
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/structure"
)

func TestSecurityRegression_GC08_FrameConditionRejectsTargetRecordAndUnsolicitedRecord(t *testing.T) {
	target := model.MustParsePath("/app/key")

	before := lkal.Record{
		Fields: []lkal.Field{{
			Key: "app",
			Value: lkal.Record{
				Fields: []lkal.Field{{Key: "key", Value: lkal.String("v1")}},
			},
		}},
	}

	// Case 1: Target path becomes Present(Record) instead of Leaf/Absent.
	afterTargetRecord := lkal.Record{
		Fields: []lkal.Field{{
			Key: "app",
			Value: lkal.Record{
				Fields: []lkal.Field{{Key: "key", Value: lkal.Record{}}},
			},
		}},
	}
	if err := structure.VerifyFrameCondition(before, afterTargetRecord, target); err == nil {
		t.Fatal("expected VerifyFrameCondition to reject target path becoming Present(Record)")
	}

	// Case 2: Unsolicited non-target path transitions Absent -> Present(Record).
	afterUnsolicited := lkal.Record{
		Fields: []lkal.Field{
			{
				Key: "app",
				Value: lkal.Record{
					Fields: []lkal.Field{{Key: "key", Value: lkal.String("v2")}},
				},
			},
			{
				Key:   "unsolicited",
				Value: lkal.Record{},
			},
		},
	}
	if err := structure.VerifyFrameCondition(before, afterUnsolicited, target); err == nil {
		t.Fatal("expected VerifyFrameCondition to reject unsolicited non-target Absent -> Present(Record)")
	}
}
