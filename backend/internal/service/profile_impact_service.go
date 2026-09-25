package service

import (
	"context"
	"net/http"
	"sort"

	"food-allergen-crosscontact-analyzer/backend/internal/analyzer"
	"food-allergen-crosscontact-analyzer/backend/internal/config"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
)

// ProfileImpactService computes a read-only what-if preview of changing one
// profile's allergen set. It never updates profiles, routes, edges, or
// assessment runs and writes no audit events; callers must persist nothing on
// its behalf before explicit user confirmation.
type ProfileImpactService struct {
	routes     repository.RouteRepository
	profiles   repository.ProfileRepository
	propagator routePropagator
}

func NewProfileImpactService(routes repository.RouteRepository, profiles repository.ProfileRepository, edges repository.ContactEdgeRepository, cfg config.Config) (*ProfileImpactService, error) {
	thresholds, err := analyzer.NewThresholdSnapshot(cfg.Thresholds)
	if err != nil {
		return nil, err
	}
	return &ProfileImpactService{routes: routes, profiles: profiles, propagator: newRoutePropagator(routes, profiles, edges, cfg.MaxPropagationDepth, thresholds)}, nil
}

// RouteVersionConflict describes a route the caller pinned at a stale version
// or failed to pin. The whole preview is rejected so the caller re-reads the
// current profile/route versions before trusting any matrix diff.
type RouteVersionConflict struct {
	RouteID        uint   `json:"route_id"`
	RouteCode      string `json:"route_code"`
	PinnedVersion  uint   `json:"pinned_version"`
	CurrentVersion uint   `json:"current_version"`
	Reason         string `json:"reason"`
}

type ImpactAllergenDelta struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

type ImpactRoutePreview struct {
	RouteID           uint                  `json:"route_id"`
	RouteCode         string                `json:"route_code"`
	ProductName       string                `json:"product_name"`
	RouteVersion      uint                  `json:"route_version"`
	ReferencingSteps  []string              `json:"referencing_steps"`
	Computed          bool                  `json:"computed"`
	ErrorCode         string                `json:"error_code,omitempty"`
	ErrorMessage      string                `json:"error_message,omitempty"`
	AllergenDelta     ImpactAllergenDelta   `json:"allergen_delta"`
	Changes           []analyzer.CellChange `json:"changes"`
	ChangeCounts      map[string]int        `json:"change_counts"`
	BiggestChange     *analyzer.CellChange  `json:"biggest_change,omitempty"`
	HighestRiskBefore string                `json:"highest_risk_before"`
	HighestRiskAfter  string                `json:"highest_risk_after"`
}

// ProfileImpactPreview is the full read-only result for one profile edit.
type ProfileImpactPreview struct {
	ProfileID              uint                 `json:"profile_id"`
	ProfileCode            string               `json:"profile_code"`
	ProfileVersion         uint                 `json:"profile_version"`
	ExpectedProfileVersion uint                 `json:"expected_profile_version"`
	AllergenDelta          ImpactAllergenDelta  `json:"allergen_delta"`
	ActiveRouteCount       int                  `json:"active_route_count"`
	ComputedRouteCount     int                  `json:"computed_route_count"`
	FailedRouteCount       int                  `json:"failed_route_count"`
	Routes                 []ImpactRoutePreview `json:"routes"`
	ChangeCounts           map[string]int       `json:"change_counts"`
	BiggestChange          *analyzer.CellChange `json:"biggest_change,omitempty"`
	BiggestChangeRouteID   uint                 `json:"biggest_change_route_id,omitempty"`
	BiggestChangeRouteCode string               `json:"biggest_change_route_code,omitempty"`
	ThresholdVersion       string               `json:"threshold_version"`
	MaxDepth               int                  `json:"max_depth"`
}

func (s *ProfileImpactService) Preview(ctx context.Context, profileID uint, request dto.ProfileImpactPreviewRequest) (ProfileImpactPreview, error) {
	profile, err := s.profiles.Get(ctx, profileID)
	if err != nil {
		return ProfileImpactPreview{}, err
	}
	if profile.Version != request.ExpectedVersion {
		conflict := NewError(http.StatusConflict, "profile_version_conflict", "过敏原谱在推演发起后已变化，请重新读取谱详情与路线版本", nil)
		return ProfileImpactPreview{}, conflict
	}

	references, err := s.profiles.ReferencingRoutes(ctx, profileID)
	if err != nil {
		return ProfileImpactPreview{}, err
	}
	pinned := make(map[uint]uint, len(request.RouteVersions))
	for _, pin := range request.RouteVersions {
		pinned[pin.RouteID] = pin.ExpectedVersion
	}
	activeIDs := make([]uint, 0, len(references))
	conflicts := make([]RouteVersionConflict, 0)
	for _, ref := range references {
		if ref.RouteStatus != "active" {
			continue
		}
		activeIDs = append(activeIDs, ref.RouteID)
		current, ok := pinned[ref.RouteID]
		if !ok {
			conflicts = append(conflicts, RouteVersionConflict{RouteID: ref.RouteID, RouteCode: ref.RouteCode, CurrentVersion: ref.RouteVersion, Reason: "route_version_missing"})
			continue
		}
		if current != ref.RouteVersion {
			conflicts = append(conflicts, RouteVersionConflict{RouteID: ref.RouteID, RouteCode: ref.RouteCode, PinnedVersion: current, CurrentVersion: ref.RouteVersion, Reason: "route_version_conflict"})
		}
	}
	if len(conflicts) > 0 {
		sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].RouteID < conflicts[j].RouteID })
		return ProfileImpactPreview{}, &AppError{Status: http.StatusConflict, Code: "route_version_conflict", Message: "引用该谱的生效路线版本在推演发起后已变化，请重新读取路线版本", Details: conflicts}
	}

	proposed, err := normalizeAllergenList(request.Allergens)
	if err != nil {
		return ProfileImpactPreview{}, NewError(http.StatusBadRequest, "validation_error", "至少需要一个有效过敏原", nil)
	}
	var current []string
	if err := unmarshalAllergensJSON(profile.AllergensJSON, &current); err != nil {
		return ProfileImpactPreview{}, NewError(http.StatusUnprocessableEntity, "profile_json_invalid", "过敏原谱内容无法解析", err)
	}
	delta := allergenDelta(current, proposed)

	routes, err := s.routes.GetMany(ctx, activeIDs)
	if err != nil {
		return ProfileImpactPreview{}, err
	}
	// Re-check versions on the fully loaded records: the profile or a route
	// could have been versioned between the opening checks and this fetch, in
	// which case the pinned "initiation versions" no longer describe what
	// would be computed and the caller must re-read.
	currentProfile, err := s.profiles.Get(ctx, profileID)
	if err != nil {
		return ProfileImpactPreview{}, err
	}
	if currentProfile.Version != request.ExpectedVersion {
		return ProfileImpactPreview{}, NewError(http.StatusConflict, "profile_version_conflict", "过敏原谱在推演发起后已变化，请重新读取谱详情与路线版本", nil)
	}
	lateConflicts := make([]RouteVersionConflict, 0)
	for _, route := range routes {
		if pinned[route.ID] != route.Version {
			lateConflicts = append(lateConflicts, RouteVersionConflict{RouteID: route.ID, RouteCode: route.RouteCode, PinnedVersion: pinned[route.ID], CurrentVersion: route.Version, Reason: "route_version_conflict"})
		}
	}
	if len(lateConflicts) > 0 {
		sort.Slice(lateConflicts, func(i, j int) bool { return lateConflicts[i].RouteID < lateConflicts[j].RouteID })
		return ProfileImpactPreview{}, &AppError{Status: http.StatusConflict, Code: "route_version_conflict", Message: "引用该谱的生效路线版本在推演发起后已变化，请重新读取路线版本", Details: lateConflicts}
	}
	preview := ProfileImpactPreview{ProfileID: profile.ID, ProfileCode: profile.ProfileCode, ProfileVersion: profile.Version, ExpectedProfileVersion: request.ExpectedVersion, AllergenDelta: delta, ActiveRouteCount: len(routes), Routes: make([]ImpactRoutePreview, 0, len(routes)), ChangeCounts: emptyChangeCounts(), ThresholdVersion: s.propagator.thresholds.Version, MaxDepth: s.propagator.maxDepth}

	for _, route := range routes {
		entry := ImpactRoutePreview{RouteID: route.ID, RouteCode: route.RouteCode, ProductName: route.ProductName, RouteVersion: route.Version, AllergenDelta: delta, ChangeCounts: emptyChangeCounts()}
		input, loadErr := s.propagator.loadRoute(ctx, route)
		if loadErr != nil {
			entry.Computed = false
			entry.ErrorCode, entry.ErrorMessage = describeComputeError(loadErr)
			preview.Routes = append(preview.Routes, entry)
			preview.FailedRouteCount++
			continue
		}
		entry.ReferencingSteps = stepsReferencingProfile(input.steps, profileID)
		baselineResult, baseErr := s.propagator.propagate(input, input.seeds)
		if baseErr != nil {
			entry.Computed = false
			entry.ErrorCode, entry.ErrorMessage = describeComputeError(baseErr)
			preview.Routes = append(preview.Routes, entry)
			preview.FailedRouteCount++
			continue
		}
		proposedSeeds := cloneSeeds(input.seeds)
		proposedSeeds[profileID] = analyzer.ProfileSeed{ProfileID: profile.ID, ProfileCode: profile.ProfileCode, MaterialName: profile.MaterialName, Version: profile.Version, Allergens: proposed}
		proposedResult, propErr := s.propagator.propagate(input, proposedSeeds)
		if propErr != nil {
			entry.Computed = false
			entry.ErrorCode, entry.ErrorMessage = describeComputeError(propErr)
			preview.Routes = append(preview.Routes, entry)
			preview.FailedRouteCount++
			continue
		}
		changes := analyzer.DiffMatrices(baselineResult, proposedResult)
		entry.Computed = true
		entry.Changes = changes
		entry.HighestRiskBefore = string(baselineResult.HighestRiskLevel)
		entry.HighestRiskAfter = string(proposedResult.HighestRiskLevel)
		for _, change := range changes {
			entry.ChangeCounts[string(change.Kind)]++
			preview.ChangeCounts[string(change.Kind)]++
		}
		if len(changes) > 0 {
			entry.BiggestChange = &changes[0]
		}
		preview.ComputedRouteCount++
		preview.Routes = append(preview.Routes, entry)
	}

	sort.Slice(preview.Routes, func(i, j int) bool { return preview.Routes[i].RouteCode < preview.Routes[j].RouteCode })
	for index := range preview.Routes {
		routeChange := preview.Routes[index]
		if routeChange.BiggestChange == nil {
			continue
		}
		if preview.BiggestChange == nil || analyzer.CompareCellChanges(*routeChange.BiggestChange, *preview.BiggestChange) {
			change := *routeChange.BiggestChange
			preview.BiggestChange = &change
			preview.BiggestChangeRouteID = routeChange.RouteID
			preview.BiggestChangeRouteCode = routeChange.RouteCode
		}
	}
	return preview, nil
}

func stepsReferencingProfile(steps []dto.RouteStep, profileID uint) []string {
	result := make([]string, 0)
	for _, step := range steps {
		if step.ProfileID == profileID {
			result = append(result, step.StepCode)
		}
	}
	return result
}

func cloneSeeds(source map[uint]analyzer.ProfileSeed) map[uint]analyzer.ProfileSeed {
	target := make(map[uint]analyzer.ProfileSeed, len(source))
	for id, seed := range source {
		target[id] = seed
	}
	return target
}

func emptyChangeCounts() map[string]int {
	return map[string]int{string(analyzer.ChangeAdded): 0, string(analyzer.ChangeRemoved): 0, string(analyzer.ChangeUpgrade): 0, string(analyzer.ChangeDowngrade): 0}
}

func describeComputeError(err error) (string, string) {
	if app, ok := err.(*AppError); ok {
		return app.Code, app.Message
	}
	return "compute_failed", err.Error()
}
