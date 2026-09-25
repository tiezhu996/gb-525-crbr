package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"

	"food-allergen-crosscontact-analyzer/backend/internal/analyzer"
	"food-allergen-crosscontact-analyzer/backend/internal/config"
	"food-allergen-crosscontact-analyzer/backend/internal/constants"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/model"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
)

const (
	impactChangeAdded      = "added"
	impactChangeRemoved    = "removed"
	impactChangeUpgraded   = "upgraded"
	impactChangeDowngraded = "downgraded"
)

// ImpactPreviewService evaluates how a not-yet-saved allergen set would change
// the cross-contact matrices of every effective route referencing a profile.
// It is strictly read-only: no profile write, no assessment mutation, no
// staling and no audit record.
type ImpactPreviewService struct {
	profiles repository.ProfileRepository
	routeAnalyzer
}

func NewImpactPreviewService(profiles repository.ProfileRepository, routes repository.RouteRepository, edges repository.ContactEdgeRepository, cfg config.Config) (*ImpactPreviewService, error) {
	thresholds, err := analyzer.NewThresholdSnapshot(cfg.Thresholds)
	if err != nil {
		return nil, err
	}
	return &ImpactPreviewService{profiles: profiles, routeAnalyzer: newRouteAnalyzer(routes, profiles, edges, cfg.MaxPropagationDepth, thresholds)}, nil
}

// Preview runs the dry run. A drift on the profile itself is a hard conflict
// (the editor is stale); per-route version drift or computation failures are
// reported as uncalculable routes while the remaining routes are evaluated.
func (s *ImpactPreviewService) Preview(ctx context.Context, profileID uint, request dto.ProfileImpactPreviewRequest) (dto.ProfileImpactPreview, error) {
	profile, err := s.profiles.Get(ctx, profileID)
	if err != nil {
		return dto.ProfileImpactPreview{}, err
	}
	if profile.Version != request.ExpectedProfileVersion {
		return dto.ProfileImpactPreview{}, NewError(http.StatusConflict, "profile_version_conflict", "过敏原谱在推演发起后已变化，请重新读取谱与引用路线", nil)
	}
	var currentAllergens []string
	if err := json.Unmarshal(profile.AllergensJSON, &currentAllergens); err != nil {
		return dto.ProfileImpactPreview{}, NewError(http.StatusUnprocessableEntity, "profile_json_invalid", "过敏原谱内容无法解析", err)
	}
	proposedEncoded, err := NormalizeAllergens(request.Allergens)
	if err != nil {
		return dto.ProfileImpactPreview{}, err
	}
	var proposedAllergens []string
	if err := json.Unmarshal(proposedEncoded, &proposedAllergens); err != nil {
		return dto.ProfileImpactPreview{}, err
	}

	activeRoutes, err := s.routes.ActiveRoutes(ctx)
	if err != nil {
		return dto.ProfileImpactPreview{}, err
	}
	response := dto.ProfileImpactPreview{
		ProfileID:          profile.ID,
		ProfileCode:        profile.ProfileCode,
		ProfileVersion:     profile.Version,
		CurrentAllergens:   canonicalAllergens(currentAllergens),
		ProposedAllergens:  proposedAllergens,
		AddedAllergens:     setDifference(proposedAllergens, currentAllergens),
		RemovedAllergens:   setDifference(currentAllergens, proposedAllergens),
		ThresholdVersion:   s.thresholds.Version,
		MaxDepth:           s.maxDepth,
		Routes:             make([]dto.ImpactRouteResult, 0),
		UncalculableRoutes: make([]dto.ImpactUncalculableRoute, 0),
	}
	override := analyzer.ProfileSeed{ProfileID: profile.ID, ProfileCode: profile.ProfileCode, MaterialName: profile.MaterialName, Version: profile.Version, Allergens: proposedAllergens}
	for _, route := range activeRoutes {
		references, err := routeReferencesProfile(route, profile.ID)
		if err != nil {
			return dto.ProfileImpactPreview{}, err
		}
		if !references {
			continue
		}
		response.EffectiveRouteCount++
		expectedVersion, known := request.ExpectedRouteVersions[route.RouteCode]
		if !known || expectedVersion == 0 {
			response.UncalculableRoutes = append(response.UncalculableRoutes, uncalculable(route, expectedVersion, "route_version_unknown", "发起推演时未提供该路线版本，请重新读取路线后再推演"))
			continue
		}
		if route.Version != expectedVersion {
			response.UncalculableRoutes = append(response.UncalculableRoutes, uncalculable(route, expectedVersion, "route_version_conflict", "路线在推演发起后已变化，请重新读取路线版本后再推演"))
			continue
		}
		routeResult, calculateErr := s.evaluateRoute(ctx, route, override)
		if calculateErr != nil {
			response.UncalculableRoutes = append(response.UncalculableRoutes, uncalculable(route, expectedVersion, impactReasonCode(calculateErr), calculateErr.Error()))
			continue
		}
		response.CalculatedRouteCount++
		response.Routes = append(response.Routes, routeResult)
		if routeResult.BiggestChange != nil {
			if response.GlobalBiggestChange == nil || compareChangeMagnitude(routeResult.BiggestChange, &response.GlobalBiggestChange.Change) > 0 {
				change := *routeResult.BiggestChange
				response.GlobalBiggestChange = &dto.GlobalImpactChange{RouteID: route.ID, RouteCode: route.RouteCode, ProductName: route.ProductName, RouteVersion: route.Version, Change: change}
			}
		}
	}
	sortUncalculable(response.UncalculableRoutes)
	return response, nil
}

func (s *ImpactPreviewService) evaluateRoute(ctx context.Context, route model.ProcessRoute, override analyzer.ProfileSeed) (dto.ImpactRouteResult, error) {
	before, err := s.analyzeRoute(ctx, route, nil)
	if err != nil {
		return dto.ImpactRouteResult{}, err
	}
	after, err := s.analyzeRoute(ctx, route, &override)
	if err != nil {
		return dto.ImpactRouteResult{}, err
	}
	changes := diffMatrices(before.Result, after.Result)
	result := dto.ImpactRouteResult{
		RouteID:            route.ID,
		RouteCode:          route.RouteCode,
		ProductName:        route.ProductName,
		RouteVersion:       route.Version,
		Changes:            changes,
		ContactEdgeVersion: impactEdgeVersions(after.Input.Edges),
	}
	for index := range changes {
		switch changes[index].ChangeType {
		case impactChangeAdded:
			result.AddedCount++
		case impactChangeRemoved:
			result.RemovedCount++
		case impactChangeUpgraded:
			result.UpgradedCount++
		case impactChangeDowngraded:
			result.DowngradedCount++
		}
	}
	if len(changes) > 0 {
		biggest := 0
		for index := 1; index < len(changes); index++ {
			if compareChangeMagnitude(&changes[index], &changes[biggest]) > 0 {
				biggest = index
			}
		}
		biggestChange := changes[biggest]
		result.BiggestChange = &biggestChange
	}
	return result, nil
}

type matrixKey struct {
	step     string
	allergen string // lowercased: allergen identity is case-insensitive
}

func cellKey(cell analyzer.MatrixCell) matrixKey {
	return matrixKey{step: cell.TargetStepCode, allergen: strings.ToLower(cell.Allergen)}
}

// diffMatrices pairs cells by target step and allergen and classifies every
// difference as added, removed, upgraded or downgraded. Allergen labels are
// matched case-insensitively so a label casing change is not reported as a
// removed plus added cell. Cells whose risk band is unchanged are intentionally
// omitted, even when the raw score drifts.
func diffMatrices(before, after analyzer.Result) []dto.ImpactCellChange {
	beforeCells := make(map[matrixKey]analyzer.MatrixCell, len(before.Matrix))
	for _, cell := range before.Matrix {
		beforeCells[cellKey(cell)] = cell
	}
	afterCells := make(map[matrixKey]analyzer.MatrixCell, len(after.Matrix))
	for _, cell := range after.Matrix {
		afterCells[cellKey(cell)] = cell
	}
	changes := make([]dto.ImpactCellChange, 0)
	for key, beforeCell := range beforeCells {
		afterCell, exists := afterCells[key]
		if !exists {
			change := dto.ImpactCellChange{ChangeType: impactChangeRemoved, TargetStepCode: key.step, TargetStepName: beforeCell.TargetStepName, Allergen: beforeCell.Allergen, Before: cellPointer(beforeCell), LevelDelta: constants.RiskRank(beforeCell.RiskLevel), ScoreDelta: -beforeCell.MaxRawScore, Declared: beforeCell.Declared, BeforeEvidence: dominantRiskItem(before.RiskItems, key)}
			changes = append(changes, change)
			continue
		}
		deltaRank := constants.RiskRank(afterCell.RiskLevel) - constants.RiskRank(beforeCell.RiskLevel)
		if deltaRank == 0 {
			continue
		}
		changeType := impactChangeUpgraded
		levelDelta := deltaRank
		if deltaRank < 0 {
			changeType = impactChangeDowngraded
			levelDelta = -deltaRank
		}
		changes = append(changes, dto.ImpactCellChange{ChangeType: changeType, TargetStepCode: key.step, TargetStepName: afterCell.TargetStepName, Allergen: afterCell.Allergen, Before: cellPointer(beforeCell), After: cellPointer(afterCell), LevelDelta: levelDelta, ScoreDelta: afterCell.MaxRawScore - beforeCell.MaxRawScore, Declared: afterCell.Declared, BeforeEvidence: dominantRiskItem(before.RiskItems, key), AfterEvidence: dominantRiskItem(after.RiskItems, key)})
	}
	for key, afterCell := range afterCells {
		if _, exists := beforeCells[key]; exists {
			continue
		}
		changes = append(changes, dto.ImpactCellChange{ChangeType: impactChangeAdded, TargetStepCode: key.step, TargetStepName: afterCell.TargetStepName, Allergen: afterCell.Allergen, After: cellPointer(afterCell), LevelDelta: constants.RiskRank(afterCell.RiskLevel), ScoreDelta: afterCell.MaxRawScore, Declared: afterCell.Declared, AfterEvidence: dominantRiskItem(after.RiskItems, key)})
	}
	sort.Slice(changes, func(i, j int) bool { return compareChangeMagnitude(&changes[i], &changes[j]) > 0 })
	return changes
}

// dominantRiskItem returns the highest-score evidence path for one cell.
// analyzer.Propagate already orders risk items by descending raw score, so the
// first match is the dominant path.
func dominantRiskItem(items []analyzer.RiskItem, key matrixKey) *analyzer.RiskItem {
	for index := range items {
		if items[index].TargetStepCode == key.step && strings.EqualFold(items[index].Allergen, key.allergen) {
			item := items[index]
			return &item
		}
	}
	return nil
}

// compareChangeMagnitude orders changes for "biggest change" selection:
// presence changes first, then band distance, then absolute score movement,
// then allergen/step code for deterministic ties.
func compareChangeMagnitude(left, right *dto.ImpactCellChange) int {
	leftPresence := 0
	if left.ChangeType == impactChangeAdded || left.ChangeType == impactChangeRemoved {
		leftPresence = 1
	}
	rightPresence := 0
	if right.ChangeType == impactChangeAdded || right.ChangeType == impactChangeRemoved {
		rightPresence = 1
	}
	if leftPresence != rightPresence {
		return leftPresence - rightPresence
	}
	if left.LevelDelta != right.LevelDelta {
		return left.LevelDelta - right.LevelDelta
	}
	leftMovement := math.Abs(left.ScoreDelta)
	rightMovement := math.Abs(right.ScoreDelta)
	if leftMovement != rightMovement {
		if leftMovement > rightMovement {
			return 1
		}
		return -1
	}
	if left.Allergen != right.Allergen {
		if left.Allergen < right.Allergen {
			return 1
		}
		return -1
	}
	if left.TargetStepCode != right.TargetStepCode {
		if left.TargetStepCode < right.TargetStepCode {
			return 1
		}
		return -1
	}
	return 0
}

func cellPointer(cell analyzer.MatrixCell) *analyzer.MatrixCell {
	copy := cell
	return &copy
}

func impactEdgeVersions(edges []model.ContactEdge) []dto.ImpactEdgeVersion {
	result := make([]dto.ImpactEdgeVersion, 0, len(edges))
	for _, edge := range edges {
		result = append(result, dto.ImpactEdgeVersion{EdgeID: edge.ID, Version: edge.Version, Enabled: edge.Enabled})
	}
	return result
}

func routeReferencesProfile(route model.ProcessRoute, profileID uint) (bool, error) {
	steps, err := DecodeRouteSteps(route)
	if err != nil {
		return false, err
	}
	for _, step := range steps {
		if step.ProfileID == profileID {
			return true, nil
		}
	}
	return false, nil
}

func uncalculable(route model.ProcessRoute, expected uint, code, message string) dto.ImpactUncalculableRoute {
	return dto.ImpactUncalculableRoute{RouteID: route.ID, RouteCode: route.RouteCode, ProductName: route.ProductName, CurrentVersion: route.Version, ExpectedVersion: expected, ReasonCode: code, ReasonMessage: message}
}

func impactReasonCode(err error) string {
	var app *AppError
	if errors.As(err, &app) {
		return app.Code
	}
	return "computation_failed"
}

func sortUncalculable(items []dto.ImpactUncalculableRoute) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].RouteCode != items[j].RouteCode {
			return items[i].RouteCode < items[j].RouteCode
		}
		return items[i].RouteID < items[j].RouteID
	})
}

func canonicalAllergens(items []string) []string {
	seen := make(map[string]string)
	for _, item := range items {
		clean := strings.TrimSpace(item)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if _, exists := seen[key]; !exists {
			seen[key] = clean
		}
	}
	result := make([]string, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// setDifference returns labels present in want but absent from have, compared
// case-insensitively and rendered with the casing of want.
func setDifference(want, have []string) []string {
	haveSet := make(map[string]struct{}, len(have))
	for _, item := range have {
		haveSet[strings.ToLower(strings.TrimSpace(item))] = struct{}{}
	}
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range want {
		key := strings.ToLower(item)
		if _, exists := haveSet[key]; exists {
			continue
		}
		if _, duplicated := seen[key]; duplicated {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}
