package probe

import (
	"math"
	"testing"
)

// "NaN", "Infinity" and 1e999 are read by strconv but are not numbers a
// rule can judge, a database can store or JSON can carry.
func TestNonFiniteValuesRefused(t *testing.T) {
	doc := []byte(`{"n":"NaN","i":"Infinity","x":1e999,"ok":"12.5"}`)
	for _, p := range []string{"$.n", "$.i", "$.x"} {
		if _, ok := jsonNumber(doc, p); ok {
			t.Errorf("%s accepted as a number", p)
		}
	}
	if f, ok := jsonNumber(doc, "$.ok"); !ok || f != 12.5 {
		t.Fatalf("a real number refused: %v %v", f, ok)
	}
	if !finite(1) || finite(math.NaN()) || finite(math.Inf(-1)) {
		t.Fatal("finite")
	}
}
