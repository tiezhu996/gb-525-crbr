package analyzer

import (
	"math"
	"sort"
	"strings"

	"food-allergen-crosscontact-analyzer/backend/internal/constants"
)

const scoreEpsilon = 1e-9

// CellChangeKind classifies how a target-step × allergen matrix cell moves
// between the baseline propagation and the proposed-allergen propagation.
//
// Upgrade/downgrade are decided first by risk rank; a cell whose rank is
// unchanged but whose max raw score moves is still reported as an
// upgrade/downgrade with RiskLevelChanged=false so the matrix never hides a
// score movement that stays inside one threshold band.
type CellChangeKind string

const (
	ChangeAdded     CellChangeKind = "added"
	ChangeRemoved   CellChangeKind = "removed"
	ChangeUpgrade   CellChangeKind = "upgrade"
	ChangeDowngrade CellChangeKind = "downgrade"
)

// CellChange describes one changed matrix cell for a single route. Before is
// nil for added cells and After is nil for removed cells. The dominant risk
// items (highest raw score path) are retained so the UI can render the full
// evidence path for the most-impactful evidence chain.
type CellChange struct {
	Kind             CellChangeKind `json:"kind"`
	TargetStepCode   string         `json:"target_step_code"`
	TargetStepName   string         `json:"target_step_name"`
	Allergen         string         `json:"allergen"`
	Declared         bool           `json:"declared"`
	Before           *MatrixCell    `json:"before,omitempty"`
	After            *MatrixCell    `json:"after,omitempty"`
	ScoreDelta       float64        `json:"score_delta"`
	RiskRankDelta    int            `json:"risk_rank_delta"`
	RiskLevelChanged bool           `json:"risk_level_changed"`
	BeforeEvidence   *RiskItem      `json:"before_evidence,omitempty"`
	AfterEvidence    *RiskItem      `json:"after_evidence,omitempty"`
}

// DiffMatrices compares the baseline result against the proposed result and
// returns added, removed, upgraded and downgraded cells. Cells that are
// identical (same max score and risk level) are intentionally omitted.
func DiffMatrices(baseline, proposed Result) []CellChange {
	beforeCells := indexCells(baseline.Matrix)
	afterCells := indexCells(proposed.Matrix)
	beforeItems := dominantItems(baseline.RiskItems)
	afterItems := dominantItems(proposed.RiskItems)

	keys := make([]cellKey, 0, len(beforeCells)+len(afterCells))
	seen := make(map[cellKey]bool, len(beforeCells)+len(afterCells))
	for key := range beforeCells {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range afterCells {
		if !seen[key] {
			keys = append(keys, key)
		}
	}

	changes := make([]CellChange, 0)
	for _, key := range keys {
		before, hadBefore := beforeCells[key]
		after, hasAfter := afterCells[key]
		switch {
		case !hadBefore && hasAfter:
			changes = append(changes, CellChange{Kind: ChangeAdded, TargetStepCode: after.TargetStepCode, TargetStepName: after.TargetStepName, Allergen: after.Allergen, Declared: after.Declared, After: cellPtr(after), ScoreDelta: after.MaxRawScore, RiskRankDelta: constants.RiskRank(after.RiskLevel), RiskLevelChanged: true, AfterEvidence: afterItems[key]})
		case hadBefore && !hasAfter:
			changes = append(changes, CellChange{Kind: ChangeRemoved, TargetStepCode: before.TargetStepCode, TargetStepName: before.TargetStepName, Allergen: before.Allergen, Declared: before.Declared, Before: cellPtr(before), ScoreDelta: -before.MaxRawScore, RiskRankDelta: -constants.RiskRank(before.RiskLevel), RiskLevelChanged: true, BeforeEvidence: beforeItems[key]})
		default:
			change, changed := compareCell(before, after, beforeItems[key], afterItems[key])
			if changed {
				changes = append(changes, change)
			}
		}
	}
	sortChanges(changes)
	return changes
}

func compareCell(before, after MatrixCell, beforeEvidence, afterEvidence *RiskItem) (CellChange, bool) {
	delta := after.MaxRawScore - before.MaxRawScore
	rankBefore := constants.RiskRank(before.RiskLevel)
	rankAfter := constants.RiskRank(after.RiskLevel)
	rankDelta := rankAfter - rankBefore
	if math.Abs(delta) < scoreEpsilon && rankDelta == 0 && before.PathCount == after.PathCount && before.Declared == after.Declared {
		return CellChange{}, false
	}
	kind := ChangeUpgrade
	if delta < 0 || rankDelta < 0 {
		kind = ChangeDowngrade
	}
	if math.Abs(delta) < scoreEpsilon && rankDelta == 0 {
		// Path-count-only movement does not change the risk picture.
		return CellChange{}, false
	}
	name := after.TargetStepName
	if name == "" {
		name = before.TargetStepName
	}
	return CellChange{Kind: kind, TargetStepCode: before.TargetStepCode, TargetStepName: name, Allergen: before.Allergen, Declared: after.Declared, Before: cellPtr(before), After: cellPtr(after), ScoreDelta: roundDelta(delta), RiskRankDelta: rankDelta, RiskLevelChanged: rankDelta != 0, BeforeEvidence: beforeEvidence, AfterEvidence: afterEvidence}, true
}

// roundDelta rounds a signed score difference to six decimal places without
// clamping negatives, unlike the propagation score rounding in score.go.
func roundDelta(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

// CompareCellChanges orders changes by absolute impact so callers can point at
// the single most-impactful change. Cross-band movement outranks in-band score
// movement; ties fall back to the raw score movement, then to a deterministic
// cell identity.
func CompareCellChanges(left, right CellChange) bool {
	leftSeverity := absInt(left.RiskRankDelta)
	rightSeverity := absInt(right.RiskRankDelta)
	if leftSeverity != rightSeverity {
		return leftSeverity > rightSeverity
	}
	leftAbs := absFloat(left.ScoreDelta)
	rightAbs := absFloat(right.ScoreDelta)
	if leftAbs != rightAbs {
		return leftAbs > rightAbs
	}
	if left.TargetStepCode != right.TargetStepCode {
		return left.TargetStepCode < right.TargetStepCode
	}
	if left.Allergen != right.Allergen {
		return left.Allergen < right.Allergen
	}
	return left.Kind < right.Kind
}

func sortChanges(changes []CellChange) {
	sort.Slice(changes, func(i, j int) bool { return CompareCellChanges(changes[i], changes[j]) })
}

type cellKey struct{ step, allergen string }

func indexCells(cells []MatrixCell) map[cellKey]MatrixCell {
	result := make(map[cellKey]MatrixCell, len(cells))
	for _, cell := range cells {
		result[cellKey{cell.TargetStepCode, cell.Allergen}] = cell
	}
	return result
}

// dominantItems maps each cell to the risk item carrying its max raw score
// (the same rule buildMatrix uses), selecting explicitly so the result does
// not depend on risk item ordering.
func dominantItems(items []RiskItem) map[cellKey]*RiskItem {
	result := make(map[cellKey]*RiskItem)
	for index := range items {
		key := cellKey{items[index].TargetStepCode, items[index].Allergen}
		current, exists := result[key]
		if !exists || riskItemDominates(items[index], *current) {
			item := items[index]
			result[key] = &item
		}
	}
	return result
}

func riskItemDominates(candidate, current RiskItem) bool {
	if candidate.RawScore != current.RawScore {
		return candidate.RawScore > current.RawScore
	}
	return strings.Join(candidate.Path, "\x00") < strings.Join(current.Path, "\x00")
}

func cellPtr(cell MatrixCell) *MatrixCell {
	copy := cell
	return &copy
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
