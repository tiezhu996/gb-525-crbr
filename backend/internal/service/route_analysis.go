package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"food-allergen-crosscontact-analyzer/backend/internal/analyzer"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/model"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
	"gorm.io/datatypes"
)

// routeAnalyzeInput is the full decoded input for one weighted-path analysis.
type routeAnalyzeInput struct {
	Route    model.ProcessRoute
	Steps    []dto.RouteStep
	Declared []string
	Edges    []model.ContactEdge
	Seeds    map[uint]analyzer.ProfileSeed
}

// routeAnalyzer builds the graph inputs shared by persisted assessments and by
// read-only previews (matrix compute and profile impact dry runs). It never
// writes results, creates runs, or marks existing assessments stale.
type routeAnalyzer struct {
	routes     repository.RouteRepository
	profiles   repository.ProfileRepository
	edges      repository.ContactEdgeRepository
	maxDepth   int
	thresholds analyzer.ThresholdSnapshot
}

func newRouteAnalyzer(routes repository.RouteRepository, profiles repository.ProfileRepository, edges repository.ContactEdgeRepository, maxDepth int, thresholds analyzer.ThresholdSnapshot) routeAnalyzer {
	return routeAnalyzer{routes: routes, profiles: profiles, edges: edges, maxDepth: maxDepth, thresholds: thresholds}
}

// loadAnalyzeInput decodes one route and resolves the enabled edges and profile
// seeds referenced by its steps. A non-nil override replaces the stored seed of
// that profile id, allowing read-only what-if evaluations without persisting.
func (a routeAnalyzer) loadAnalyzeInput(ctx context.Context, route model.ProcessRoute, override *analyzer.ProfileSeed) (routeAnalyzeInput, error) {
	steps, err := DecodeRouteSteps(route)
	if err != nil {
		return routeAnalyzeInput{}, err
	}
	declared, err := DecodeDeclared(route)
	if err != nil {
		return routeAnalyzeInput{}, err
	}
	edges, err := a.edges.ForRoute(ctx, route.ID)
	if err != nil {
		return routeAnalyzeInput{}, err
	}
	ids := make([]uint, 0, len(steps))
	seen := make(map[uint]bool)
	for _, step := range steps {
		if !seen[step.ProfileID] {
			seen[step.ProfileID] = true
			ids = append(ids, step.ProfileID)
		}
	}
	profiles, err := a.profiles.GetMany(ctx, ids)
	if err != nil {
		return routeAnalyzeInput{}, err
	}
	if len(profiles) != len(ids) {
		return routeAnalyzeInput{}, NewError(http.StatusUnprocessableEntity, "profile_missing", "路线引用的过敏原谱已不可用", nil)
	}
	seeds := make(map[uint]analyzer.ProfileSeed, len(profiles))
	for _, profile := range profiles {
		var allergens []string
		if err := json.Unmarshal(profile.AllergensJSON, &allergens); err != nil {
			return routeAnalyzeInput{}, NewError(http.StatusUnprocessableEntity, "profile_json_invalid", "过敏原谱内容无法解析", err)
		}
		seed := analyzer.ProfileSeed{ProfileID: profile.ID, ProfileCode: profile.ProfileCode, MaterialName: profile.MaterialName, Version: profile.Version, Allergens: allergens}
		if override != nil && override.ProfileID == profile.ID {
			seed.Allergens = override.Allergens
		}
		seeds[profile.ID] = seed
	}
	return routeAnalyzeInput{Route: route, Steps: steps, Declared: declared, Edges: edges, Seeds: seeds}, nil
}

// analyzeResult pairs the propagation result with the resolved input so callers
// can inspect the exact profile and edge versions used by the computation.
type analyzeResult struct {
	Result analyzer.Result
	Input  routeAnalyzeInput
}

func (a routeAnalyzer) analyzeRoute(ctx context.Context, route model.ProcessRoute, override *analyzer.ProfileSeed) (analyzeResult, error) {
	input, err := a.loadAnalyzeInput(ctx, route, override)
	if err != nil {
		return analyzeResult{}, err
	}
	graph, err := analyzer.BuildGraph(analyzerSteps(input.Steps), input.Edges)
	if err != nil {
		return analyzeResult{}, NewError(http.StatusUnprocessableEntity, "graph_invalid", "接触图结构无效", err)
	}
	result, err := analyzer.Propagate(graph, input.Seeds, input.Declared, a.maxDepth, a.thresholds)
	if err != nil {
		return analyzeResult{}, NewError(http.StatusUnprocessableEntity, "propagation_failed", "风险传播计算失败", err)
	}
	return analyzeResult{Result: result, Input: input}, nil
}

func (a routeAnalyzer) analyze(ctx context.Context, routeID uint, override *analyzer.ProfileSeed) (analyzeResult, error) {
	route, err := a.routes.Get(ctx, routeID)
	if err != nil {
		return analyzeResult{}, err
	}
	return a.analyzeRoute(ctx, route, override)
}

// buildSnapshot renders the input snapshot persisted by assessment runs.
func (a routeAnalyzer) buildSnapshot(analyzed analyzeResult, algorithm string) (datatypes.JSON, error) {
	profileVersions := make([]map[string]any, 0, len(analyzed.Input.Seeds))
	for _, seed := range analyzed.Input.Seeds {
		profileVersions = append(profileVersions, map[string]any{"id": seed.ProfileID, "code": seed.ProfileCode, "version": seed.Version})
	}
	sort.Slice(profileVersions, func(i, j int) bool { return profileVersions[i]["id"].(uint) < profileVersions[j]["id"].(uint) })
	edgeVersions := make([]map[string]any, 0, len(analyzed.Input.Edges))
	for _, edge := range analyzed.Input.Edges {
		edgeVersions = append(edgeVersions, map[string]any{"id": edge.ID, "version": edge.Version, "enabled": edge.Enabled})
	}
	route := analyzed.Input.Route
	snapshotValue := map[string]any{
		"captured_at":       time.Now().UTC(),
		"route":             map[string]any{"id": route.ID, "code": route.RouteCode, "version": route.Version, "steps": analyzed.Input.Steps, "declared_allergens": analyzed.Input.Declared},
		"profiles":          profileVersions,
		"contact_edges":     edgeVersions,
		"thresholds":        a.thresholds,
		"algorithm_version": algorithm,
		"max_depth":         a.maxDepth,
		"cycles":            analyzed.Result.Cycles,
	}
	encoded, err := json.Marshal(snapshotValue)
	if err != nil {
		return nil, fmt.Errorf("encode input snapshot: %w", err)
	}
	return datatypes.JSON(encoded), nil
}

func analyzerSteps(steps []dto.RouteStep) []analyzer.StepInput {
	result := make([]analyzer.StepInput, len(steps))
	for index, step := range steps {
		result[index] = analyzer.StepInput{StepCode: step.StepCode, StepName: step.StepName, ProfileID: step.ProfileID}
	}
	return result
}
