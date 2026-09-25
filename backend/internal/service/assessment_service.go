package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"food-allergen-crosscontact-analyzer/backend/internal/analyzer"
	"food-allergen-crosscontact-analyzer/backend/internal/config"
	"food-allergen-crosscontact-analyzer/backend/internal/constants"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/model"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
	"gorm.io/datatypes"
)

type AssessmentService struct {
	runs       repository.AssessmentRepository
	routes     repository.RouteRepository
	profiles   repository.ProfileRepository
	edges      repository.ContactEdgeRepository
	propagator routePropagator
	maxDepth   int
	thresholds analyzer.ThresholdSnapshot
	algorithm  string
}

type queuedAssessmentSnapshot struct {
	RouteID             uint `json:"route_id"`
	RouteVersionAtQueue uint `json:"route_version_at_queue"`
}

func NewAssessmentService(runs repository.AssessmentRepository, routes repository.RouteRepository, profiles repository.ProfileRepository, edges repository.ContactEdgeRepository, cfg config.Config) (*AssessmentService, error) {
	thresholds, err := analyzer.NewThresholdSnapshot(cfg.Thresholds)
	if err != nil {
		return nil, fmt.Errorf("initialize thresholds: %w", err)
	}
	return &AssessmentService{runs: runs, routes: routes, profiles: profiles, edges: edges, propagator: newRoutePropagator(routes, profiles, edges, cfg.MaxPropagationDepth, thresholds), maxDepth: cfg.MaxPropagationDepth, thresholds: thresholds, algorithm: "weighted-path-v1/" + thresholds.Version}, nil
}

func (s *AssessmentService) Preview(ctx context.Context, routeID uint) (analyzer.Result, error) {
	result, _, err := s.compute(ctx, routeID)
	return result, err
}

func (s *AssessmentService) Create(ctx context.Context, request dto.CreateAssessmentRequest, actor Principal, requestID string) (model.AssessmentRun, error) {
	route, err := s.routes.Get(ctx, request.RouteID)
	if err != nil {
		return model.AssessmentRun{}, err
	}
	if route.RouteStatus != "active" {
		return model.AssessmentRun{}, NewError(http.StatusConflict, "route_inactive", "只有 active 路线可以提交评估", nil)
	}
	queuedSnapshot, err := json.Marshal(map[string]any{"route_id": route.ID, "route_version_at_queue": route.Version, "queued_at": time.Now().UTC(), "threshold_version": s.thresholds.Version})
	if err != nil {
		return model.AssessmentRun{}, fmt.Errorf("encode queued snapshot: %w", err)
	}
	run := model.AssessmentRun{RouteID: route.ID, AssessmentStatus: constants.AssessmentQueued, InputSnapshotJSON: datatypes.JSON(queuedSnapshot), MatrixJSON: datatypes.JSON([]byte("[]")), RiskItemsJSON: datatypes.JSON([]byte("[]")), HighestRiskLevel: constants.RiskLow, AlgorithmVersion: s.algorithm, CreatedBy: actor.ID}
	if err := s.runs.Create(ctx, &run, AuditScope(actor, requestID)); err != nil {
		return model.AssessmentRun{}, err
	}
	return run, nil
}

func (s *AssessmentService) Run(ctx context.Context, id uint, actor Principal, requestID string) (model.AssessmentRun, error) {
	if err := s.runs.BeginCalculation(ctx, id, AuditScope(actor, requestID)); err != nil {
		return model.AssessmentRun{}, err
	}
	run, err := s.runs.Get(ctx, id)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, err
	}
	var queued queuedAssessmentSnapshot
	if err := json.Unmarshal(run.InputSnapshotJSON, &queued); err != nil || queued.RouteID != run.RouteID || queued.RouteVersionAtQueue == 0 {
		validationErr := NewError(http.StatusUnprocessableEntity, "assessment_snapshot_invalid", "评估排队快照无效，请重新提交评估", err)
		s.resetAfterFailure(ctx, id, validationErr, actor, requestID)
		return model.AssessmentRun{}, validationErr
	}
	route, err := s.routes.Get(ctx, run.RouteID)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, err
	}
	if route.Version != queued.RouteVersionAtQueue {
		conflictErr := NewError(http.StatusConflict, "route_version_conflict", "路线在评估排队后已变化，请重新提交评估", nil)
		s.resetAfterFailure(ctx, id, conflictErr, actor, requestID)
		return model.AssessmentRun{}, conflictErr
	}
	result, snapshot, err := s.computeRoute(ctx, route)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, err
	}
	matrixJSON, err := json.Marshal(result.Matrix)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, fmt.Errorf("encode assessment matrix: %w", err)
	}
	riskJSON, err := json.Marshal(result.RiskItems)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, fmt.Errorf("encode assessment risk items: %w", err)
	}
	if err := s.runs.CompleteCalculation(ctx, id, snapshot, datatypes.JSON(matrixJSON), datatypes.JSON(riskJSON), result.HighestRiskLevel, s.algorithm, AuditScope(actor, requestID)); err != nil {
		return model.AssessmentRun{}, err
	}
	return s.runs.Get(ctx, id)
}

func (s *AssessmentService) Review(ctx context.Context, id uint, request dto.ReviewAssessmentRequest, actor Principal, requestID string) (model.AssessmentRun, error) {
	if !CanReview(actor.Role) {
		return model.AssessmentRun{}, NewError(http.StatusForbidden, "forbidden", "仅 reviewer 或 admin 可复核评估", nil)
	}
	target := constants.AssessmentStatus(request.Decision)
	if target != constants.AssessmentAccepted && target != constants.AssessmentRejected {
		return model.AssessmentRun{}, NewError(http.StatusBadRequest, "invalid_decision", "复核决定无效", nil)
	}
	if err := s.runs.Review(ctx, id, target, actor.ID, request.Reason, AuditScope(actor, requestID)); err != nil {
		return model.AssessmentRun{}, err
	}
	return s.runs.Get(ctx, id)
}

func (s *AssessmentService) Get(ctx context.Context, id uint) (model.AssessmentRun, error) {
	return s.runs.Get(ctx, id)
}
func (s *AssessmentService) List(ctx context.Context, query dto.AssessmentQuery) ([]model.AssessmentRun, int64, error) {
	return s.runs.List(ctx, query)
}
func (s *AssessmentService) Summary(ctx context.Context) (dto.AssessmentSummary, error) {
	return s.runs.Summary(ctx)
}

func (s *AssessmentService) compute(ctx context.Context, routeID uint) (analyzer.Result, datatypes.JSON, error) {
	route, err := s.routes.Get(ctx, routeID)
	if err != nil {
		return analyzer.Result{}, nil, err
	}
	return s.computeRoute(ctx, route)
}

func (s *AssessmentService) computeRoute(ctx context.Context, route model.ProcessRoute) (analyzer.Result, datatypes.JSON, error) {
	input, err := s.propagator.loadRoute(ctx, route)
	if err != nil {
		return analyzer.Result{}, nil, err
	}
	result, err := s.propagator.propagate(input, input.seeds)
	if err != nil {
		return analyzer.Result{}, nil, err
	}
	profileVersions := make([]map[string]any, 0, len(input.profiles))
	for _, profile := range input.profiles {
		profileVersions = append(profileVersions, map[string]any{"id": profile.ID, "code": profile.ProfileCode, "version": profile.Version})
	}
	sort.Slice(profileVersions, func(i, j int) bool { return profileVersions[i]["id"].(uint) < profileVersions[j]["id"].(uint) })
	edgeVersions := make([]map[string]any, 0, len(input.edges))
	for _, edge := range input.edges {
		edgeVersions = append(edgeVersions, map[string]any{"id": edge.ID, "version": edge.Version, "enabled": edge.Enabled})
	}
	snapshotValue := map[string]any{"captured_at": time.Now().UTC(), "route": map[string]any{"id": route.ID, "code": route.RouteCode, "version": route.Version, "steps": input.steps, "declared_allergens": input.declared}, "profiles": profileVersions, "contact_edges": edgeVersions, "thresholds": s.thresholds, "algorithm_version": s.algorithm, "max_depth": s.maxDepth, "cycles": result.Cycles}
	snapshot, err := json.Marshal(snapshotValue)
	if err != nil {
		return analyzer.Result{}, nil, fmt.Errorf("encode input snapshot: %w", err)
	}
	return result, datatypes.JSON(snapshot), nil
}

func (s *AssessmentService) resetAfterFailure(ctx context.Context, id uint, calculationErr error, actor Principal, requestID string) {
	_ = s.runs.ResetCalculation(ctx, id, calculationErr.Error(), AuditScope(actor, requestID))
}
