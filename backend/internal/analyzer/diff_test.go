package analyzer

import (
	"testing"

	"food-allergen-crosscontact-analyzer/backend/internal/constants"
)

func TestDiffMatrices(t *testing.T) {
	cell := func(step, allergen string, score float64, level constants.RiskLevel, declared bool) MatrixCell {
		return MatrixCell{TargetStepCode: step, TargetStepName: "Step " + step, Allergen: allergen, MaxRawScore: score, RiskLevel: level, PathCount: 1, Declared: declared}
	}
	item := func(step, allergen string, score float64) RiskItem {
		return RiskItem{SourceStepCode: "A", TargetStepCode: step, TargetStepName: "Step " + step, Allergen: allergen, Path: []string{"A", step}, RawScore: score, RiskLevel: constants.RiskLow}
	}
	tests := []struct {
		name            string
		before, after   []MatrixCell
		beforeItems     []RiskItem
		afterItems      []RiskItem
		wantKinds       map[string]CellChangeKind
		wantDelta       map[string]float64
		wantRankChanged map[string]bool
	}{
		{
			name:        "added and removed cells",
			before:      []MatrixCell{cell("B", "Milk", .2, constants.RiskMedium, false)},
			after:       []MatrixCell{cell("B", "Peanut", .5, constants.RiskHigh, false)},
			beforeItems: []RiskItem{item("B", "Milk", .2)},
			afterItems:  []RiskItem{item("B", "Peanut", .5)},
			wantKinds:   map[string]CellChangeKind{"B:Milk": ChangeRemoved, "B:Peanut": ChangeAdded},
			wantDelta:   map[string]float64{"B:Milk": -.2, "B:Peanut": .5},
		},
		{
			name:            "upgrade crosses risk band",
			before:          []MatrixCell{cell("C", "Egg", .1, constants.RiskLow, false)},
			after:           []MatrixCell{cell("C", "Egg", .4, constants.RiskHigh, false)},
			beforeItems:     []RiskItem{item("C", "Egg", .1)},
			afterItems:      []RiskItem{item("C", "Egg", .4)},
			wantKinds:       map[string]CellChangeKind{"C:Egg": ChangeUpgrade},
			wantDelta:       map[string]float64{"C:Egg": .3},
			wantRankChanged: map[string]bool{"C:Egg": true},
		},
		{
			name:            "downgrade stays inside risk band",
			before:          []MatrixCell{cell("C", "Egg", .2, constants.RiskMedium, false)},
			after:           []MatrixCell{cell("C", "Egg", .15, constants.RiskMedium, false)},
			beforeItems:     []RiskItem{item("C", "Egg", .2)},
			afterItems:      []RiskItem{item("C", "Egg", .15)},
			wantKinds:       map[string]CellChangeKind{"C:Egg": ChangeDowngrade},
			wantDelta:       map[string]float64{"C:Egg": -.05},
			wantRankChanged: map[string]bool{"C:Egg": false},
		},
		{
			name:        "identical cells are omitted",
			before:      []MatrixCell{cell("D", "Soy", .2, constants.RiskMedium, true)},
			after:       []MatrixCell{cell("D", "Soy", .2, constants.RiskMedium, true)},
			beforeItems: []RiskItem{item("D", "Soy", .2)},
			afterItems:  []RiskItem{item("D", "Soy", .2)},
			wantKinds:   map[string]CellChangeKind{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changes := DiffMatrices(Result{Matrix: test.before, RiskItems: test.beforeItems}, Result{Matrix: test.after, RiskItems: test.afterItems})
			got := make(map[string]CellChangeKind, len(changes))
			for _, change := range changes {
				key := change.TargetStepCode + ":" + change.Allergen
				got[key] = change.Kind
				if want, ok := test.wantDelta[key]; ok && change.ScoreDelta != want {
					t.Fatalf("%s delta = %v, want %v", key, change.ScoreDelta, want)
				}
				if want, ok := test.wantRankChanged[key]; ok && change.RiskLevelChanged != want {
					t.Fatalf("%s risk_level_changed = %v, want %v", key, change.RiskLevelChanged, want)
				}
			}
			if len(got) != len(test.wantKinds) {
				t.Fatalf("change count = %d (%v), want %d (%v)", len(got), got, len(test.wantKinds), test.wantKinds)
			}
			for key, kind := range test.wantKinds {
				if got[key] != kind {
					t.Fatalf("%s kind = %q, want %q", key, got[key], kind)
				}
			}
		})
	}
}

func TestDiffMatricesOrdersByImpact(t *testing.T) {
	cell := func(step, allergen string, score float64, level constants.RiskLevel) MatrixCell {
		return MatrixCell{TargetStepCode: step, TargetStepName: "Step " + step, Allergen: allergen, MaxRawScore: score, RiskLevel: level, PathCount: 1}
	}
	before := []MatrixCell{cell("B", "Milk", .2, constants.RiskMedium), cell("C", "Egg", .05, constants.RiskLow)}
	after := []MatrixCell{cell("B", "Milk", .25, constants.RiskMedium), cell("C", "Egg", .7, constants.RiskCritical)}
	changes := DiffMatrices(Result{Matrix: before}, Result{Matrix: after})
	if len(changes) != 2 {
		t.Fatalf("change count = %d, want 2", len(changes))
	}
	// Cross-band jump low -> critical outranks an in-band score increase.
	if changes[0].TargetStepCode != "C" || changes[0].Allergen != "Egg" {
		t.Fatalf("most impactful change = %s:%s, want C:Egg", changes[0].TargetStepCode, changes[0].Allergen)
	}
}

func TestDiffMatricesKeepsDominantEvidence(t *testing.T) {
	cell := MatrixCell{TargetStepCode: "B", TargetStepName: "Step B", Allergen: "Milk", MaxRawScore: .4, RiskLevel: constants.RiskHigh, PathCount: 2}
	weak := RiskItem{TargetStepCode: "B", Allergen: "Milk", Path: []string{"A", "B"}, RawScore: .1}
	strong := RiskItem{TargetStepCode: "B", Allergen: "Milk", Path: []string{"A", "X", "B"}, RawScore: .4}
	changes := DiffMatrices(Result{}, Result{Matrix: []MatrixCell{cell}, RiskItems: []RiskItem{weak, strong}})
	if len(changes) != 1 || changes[0].AfterEvidence == nil {
		t.Fatalf("expected one added change with evidence, got %+v", changes)
	}
	if changes[0].AfterEvidence.RawScore != .4 {
		t.Fatalf("evidence score = %v, want dominant 0.4", changes[0].AfterEvidence.RawScore)
	}
}
