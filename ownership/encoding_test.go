package ownership_test

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/kisage/model"
	"github.com/Kaikei-e/kisage/ownership"
)

// #7, #25, #34, #38: serialization must preserve exact typed values,
// ownership provenance, pending preconditions, and released-path revisions.
func TestOwnershipEncodingRoundTrip(t *testing.T) {
	values := []lkal.Value{lkal.String("a\nb"), lkal.Int(math.MaxInt64), lkal.Decimal("1.20"), lkal.Bool(true), lkal.Enum("ready"), lkal.Quantity{Number: "512", Unit: "Mi"}, lkal.Ref{"secret", "a/b"}, lkal.List{}, lkal.List{lkal.Decimal("1.00"), lkal.Decimal("2.10")}}
	for _, value := range values {
		e, err := ownership.NewSettledCreated("actor", value, 4)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := ownership.BeginSetUpdate(e, "update", lkal.String("next"), 5)
		if err != nil {
			t.Fatal(err)
		}
		del, err := ownership.BeginDelete(e, "delete", 6)
		if err != nil {
			t.Fatal(err)
		}
		create, err := ownership.NewPendingCreate("actor", "create", value, 3)
		if err != nil {
			t.Fatal(err)
		}
		original := ownership.NewStore()
		for path, entry := range map[string]ownership.OwnerEntry{"/settled": e, "/update": pending, "/delete": del, "/create": create, "/released": e} {
			original, err = original.Put("res", model.MustParsePath(path), entry)
			if err != nil {
				t.Fatal(err)
			}
		}
		original, err = original.Delete("res", model.MustParsePath("/released"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var restored ownership.Store
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatalf("%T: %v", value, err)
		}
		for _, path := range original.Paths("res") {
			want, _ := original.Get("res", path)
			got, ok := restored.Get("res", path)
			if !ok || !got.Equal(want) || got.Revision != want.Revision {
				t.Fatalf("lost %T at %s: got=%+v want=%+v", value, path, got, want)
			}
		}
		released := model.MustParsePath("/released")
		if restored.Has("res", released) || restored.Revision("res", released) != original.Revision("res", released) {
			t.Fatal("released-path tombstone lost")
		}
		again, err := json.Marshal(&restored)
		if err != nil || !bytes.Equal(encoded, again) {
			t.Fatalf("encoding not canonical: %v", err)
		}
	}
}

func TestOwnershipEncodingRejectsBrokenHistory(t *testing.T) {
	for _, bad := range []string{
		`null`,
		`{"version":2,"rows":[]}`,
		`{"version":1,"rows":[{"resource":"res","path":["a"],"revision":0}]}`,
		`{"version":1,"rows":[{"resource":"res","path":["a"],"revision":1},{"resource":"res","path":["a"],"revision":2}]}`,
		`{"version":1,"rows":[{"resource":"res","path":["a"],"revision":1,"entry":{"manager":"actor","origin":0}}]}`,
		`{"version":1,"rows":[],"unknown":true}`,
		`{"version":1,"rows":[]} {}`,
	} {
		store := ownership.NewStore()
		if err := json.Unmarshal([]byte(bad), store); err == nil {
			t.Fatalf("accepted invalid history: %s", bad)
		}
		if len(store.Resources()) != 0 {
			t.Fatal("rejected decode changed receiver")
		}
	}
}
