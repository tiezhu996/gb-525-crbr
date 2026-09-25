package service

import (
	"context"
	"encoding/json"
	"net/http"

	"food-allergen-crosscontact-analyzer/backend/internal/analyzer"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/model"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
)

// routePropagationInput is the fully loaded, graph-validated input for one
// propagation run. It is shared by the assessment service (which persists a
// snapshot) and the read-only profile impact preview (which never writes).
type routePropagationInput struct {
	route    model.ProcessRoute
	steps    []dto.RouteStep
	declared []string
	edges    []model.ContactEdge
	graph    analyzer.Graph
	seeds    map[uint]analyzer.ProfileSeed
	profiles []model.AllergenProfile
}

type routePropagator struct {
	routes     repository.RouteRepository
	profiles   repository.ProfileRepository
	edges      repository.ContactEdgeRepository
	maxDepth   int
	thresholds analyzer.ThresholdSnapshot
}

func newRoutePropagator(routes repository.RouteRepository, profiles repository.ProfileRepository, edges repository.ContactEdgeRepository, maxDepth int, thresholds analyzer.ThresholdSnapshot) routePropagator {
	return routePropagator{routes: routes, profiles: profiles, edges: edges, maxDepth: maxDepth, thresholds: thresholds}
}

// loadRoute validates the route graph and decodes every referenced profile.
// It performs no writes.
func (p routePropagator) loadRoute(ctx context.Context, route model.ProcessRoute) (routePropagationInput, error) {
	steps, err := DecodeRouteSteps(route)
	if err != nil {
		return routePropagationInput{}, err
	}
	declared, err := DecodeDeclared(route)
	if err != nil {
		return routePropagationInput{}, err
	}
	edges, err := p.edges.ForRoute(ctx, route.ID)
	if err != nil {
		return routePropagationInput{}, err
	}
	ids := make([]uint, 0, len(steps))
	seen := make(map[uint]bool)
	for _, step := range steps {
		if !seen[step.ProfileID] {
			seen[step.ProfileID] = true
			ids = append(ids, step.ProfileID)
		}
	}
	profiles, err := p.profiles.GetMany(ctx, ids)
	if err != nil {
		return routePropagationInput{}, err
	}
	if len(profiles) != len(ids) {
		return routePropagationInput{}, NewError(http.StatusUnprocessableEntity, "profile_missing", "路线引用的过敏原谱已不可用", nil)
	}
	seeds := make(map[uint]analyzer.ProfileSeed, len(profiles))
	for _, profile := range profiles {
		var allergens []string
		if err := json.Unmarshal(profile.AllergensJSON, &allergens); err != nil {
			return routePropagationInput{}, NewError(http.StatusUnprocessableEntity, "profile_json_invalid", "过敏原谱内容无法解析", err)
		}
		seeds[profile.ID] = analyzer.ProfileSeed{ProfileID: profile.ID, ProfileCode: profile.ProfileCode, MaterialName: profile.MaterialName, Version: profile.Version, Allergens: allergens}
	}
	graph, err := analyzer.BuildGraph(steps, edges)
	if err != nil {
		return routePropagationInput{}, NewError(http.StatusUnprocessableEntity, "graph_invalid", "接触图结构无效", err)
	}
	return routePropagationInput{route: route, steps: steps, declared: declared, edges: edges, graph: graph, seeds: seeds, profiles: profiles}, nil
}

func (p routePropagator) propagate(input routePropagationInput, seeds map[uint]analyzer.ProfileSeed) (analyzer.Result, error) {
	result, err := analyzer.Propagate(input.graph, seeds, input.declared, p.maxDepth, p.thresholds)
	if err != nil {
		return analyzer.Result{}, NewError(http.StatusUnprocessableEntity, "propagation_failed", "风险传播计算失败", err)
	}
	return result, nil
}
