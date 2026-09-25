package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	runs repository.AssessmentRepository
	routeAnalyzer
	algorithm string
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
	algorithm := "weighted-path-v1/" + thresholds.Version
	return &AssessmentService{runs: runs, routeAnalyzer: newRouteAnalyzer(routes, profiles, edges, cfg.MaxPropagationDepth, thresholds), algorithm: algorithm}, nil
}

func (s *AssessmentService) Preview(ctx context.Context, routeID uint) (analyzer.Result, error) {
	result, err := s.analyze(ctx, routeID, nil)
	if err != nil {
		return analyzer.Result{}, err
	}
	return result.Result, nil
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
	analyzed, err := s.analyzeRoute(ctx, route, nil)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, err
	}
	matrixJSON, err := json.Marshal(analyzed.Result.Matrix)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, fmt.Errorf("encode assessment matrix: %w", err)
	}
	riskJSON, err := json.Marshal(analyzed.Result.RiskItems)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, fmt.Errorf("encode assessment risk items: %w", err)
	}
	snapshot, err := s.buildSnapshot(analyzed, s.algorithm)
	if err != nil {
		s.resetAfterFailure(ctx, id, err, actor, requestID)
		return model.AssessmentRun{}, err
	}
	if err := s.runs.CompleteCalculation(ctx, id, snapshot, datatypes.JSON(matrixJSON), datatypes.JSON(riskJSON), analyzed.Result.HighestRiskLevel, s.algorithm, AuditScope(actor, requestID)); err != nil {
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

func (s *AssessmentService) resetAfterFailure(ctx context.Context, id uint, calculationErr error, actor Principal, requestID string) {
	_ = s.runs.ResetCalculation(ctx, id, calculationErr.Error(), AuditScope(actor, requestID))
}
