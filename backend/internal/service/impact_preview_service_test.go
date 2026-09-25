package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"food-allergen-crosscontact-analyzer/backend/internal/config"
	"food-allergen-crosscontact-analyzer/backend/internal/constants"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/model"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type impactFixture struct {
	service *ImpactPreviewService
	profile model.AllergenProfile
	route   model.ProcessRoute
	other   model.AllergenProfile
}

func newImpactFixture(t *testing.T) impactFixture {
	t.Helper()
	dsn := fmt.Sprintf("file:impact-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AllergenProfile{}, &model.ProcessRoute{}, &model.ContactEdge{}, &model.AssessmentRun{}, &model.AuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	scope := repository.AuditContext{RequestID: "test", ActorID: 1, ActorName: "tester"}
	profiles := repository.NewProfileRepository(db)
	routes := repository.NewRouteRepository(db)
	edges := repository.NewContactEdgeRepository(db)

	target := model.AllergenProfile{ProfileCode: "MAT-SESAME", MaterialName: "Sesame paste", AllergensJSON: datatypes.JSON([]byte(`["sesame"]`)), SourceType: "supplier_statement", ProfileStatus: "active", Version: 1, CreatedBy: 1}
	if err := profiles.Create(ctx, &target, scope); err != nil {
		t.Fatalf("create target profile: %v", err)
	}
	other := model.AllergenProfile{ProfileCode: "MAT-PEANUT", MaterialName: "Peanut butter", AllergensJSON: datatypes.JSON([]byte(`["peanut"]`)), SourceType: "supplier_statement", ProfileStatus: "active", Version: 1, CreatedBy: 1}
	if err := profiles.Create(ctx, &other, scope); err != nil {
		t.Fatalf("create other profile: %v", err)
	}
	steps := datatypes.JSON([]byte(fmt.Sprintf(`[{"step_code":"MIX-01","step_name":"Mixing","profile_id":%d},{"step_code":"FILL-02","step_name":"Filling","profile_id":%d}]`, target.ID, other.ID)))
	route := model.ProcessRoute{RouteCode: "RTE-COOKIE", ProductName: "Cookie line", OrderedStepsJSON: steps, DeclaredAllergensJSON: datatypes.JSON([]byte(`["peanut"]`)), RouteStatus: "active", Version: 1, OwnerID: 1}
	if err := routes.Create(ctx, &route, scope); err != nil {
		t.Fatalf("create route: %v", err)
	}
	edge := model.ContactEdge{RouteID: route.ID, FromStepCode: "MIX-01", ToStepCode: "FILL-02", ContactType: "shared_equipment", SharedEquipment: "Mixer A", CleaningFactor: 0.4, CarryoverProbability: 0.5, EvidenceNote: "CIP log 2026-09", Enabled: true, Version: 1, CreatedBy: 1}
	if err := edges.Create(ctx, &edge, scope); err != nil {
		t.Fatalf("create edge: %v", err)
	}
	cfg := config.Config{MaxPropagationDepth: 12, Thresholds: config.Thresholds{Medium: 0.12, High: 0.35, Critical: 0.65, Version: "2026.1"}}
	service, err := NewImpactPreviewService(profiles, routes, edges, cfg)
	if err != nil {
		t.Fatalf("new impact preview service: %v", err)
	}
	return impactFixture{service: service, profile: target, route: route, other: other}
}

func (f impactFixture) request(allergens []string) dto.ProfileImpactPreviewRequest {
	return dto.ProfileImpactPreviewRequest{Allergens: allergens, ExpectedProfileVersion: f.profile.Version, ExpectedRouteVersions: map[string]uint{f.route.RouteCode: f.route.Version}}
}

func TestImpactPreviewDiffsMatrixCells(t *testing.T) {
	fixture := newImpactFixture(t)
	preview, err := fixture.service.Preview(context.Background(), fixture.profile.ID, fixture.request([]string{"sesame", "almond"}))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.EffectiveRouteCount != 1 || preview.CalculatedRouteCount != 1 {
		t.Fatalf("route counts = %d/%d, want 1/1", preview.EffectiveRouteCount, preview.CalculatedRouteCount)
	}
	if len(preview.UncalculableRoutes) != 0 {
		t.Fatalf("uncalculable routes = %+v, want none", preview.UncalculableRoutes)
	}
	if got := preview.AddedAllergens; len(got) != 1 || got[0] != "almond" {
		t.Fatalf("added allergens = %v, want [almond]", got)
	}
	if len(preview.RemovedAllergens) != 0 {
		t.Fatalf("removed allergens = %v, want none", preview.RemovedAllergens)
	}
	if len(preview.Routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(preview.Routes))
	}
	route := preview.Routes[0]
	if route.AddedCount != 1 || route.RemovedCount != 0 || route.UpgradedCount != 0 || route.DowngradedCount != 0 {
		t.Fatalf("counts = +%d -%d ^%d v%d, want +1 -0 ^0 v0", route.AddedCount, route.RemovedCount, route.UpgradedCount, route.DowngradedCount)
	}
	if len(route.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(route.Changes))
	}
	change := route.Changes[0]
	if change.ChangeType != "added" || change.TargetStepCode != "FILL-02" || change.Allergen != "almond" {
		t.Fatalf("change = %+v, want added FILL-02/almond", change)
	}
	if change.After == nil || change.After.RiskLevel != constants.RiskMedium {
		t.Fatalf("after cell = %+v, want medium", change.After)
	}
	if change.Before != nil {
		t.Fatalf("before cell = %+v, want nil for added", change.Before)
	}
	if change.AfterEvidence == nil || len(change.AfterEvidence.Path) != 2 || change.AfterEvidence.Path[0] != "MIX-01" || change.AfterEvidence.Path[1] != "FILL-02" {
		t.Fatalf("after evidence = %+v, want path MIX-01 -> FILL-02", change.AfterEvidence)
	}
	if change.AfterEvidence.SourceProfileID != fixture.profile.ID {
		t.Fatalf("evidence source profile = %d, want %d", change.AfterEvidence.SourceProfileID, fixture.profile.ID)
	}
	if preview.GlobalBiggestChange == nil {
		t.Fatal("global biggest change missing")
	}
	if preview.GlobalBiggestChange.RouteCode != fixture.route.RouteCode || preview.GlobalBiggestChange.Change.Allergen != "almond" {
		t.Fatalf("global biggest = %+v, want RTE-COOKIE almond", preview.GlobalBiggestChange)
	}
	if preview.GlobalBiggestChange.Change.AfterEvidence == nil {
		t.Fatal("global biggest change carries no evidence path")
	}
	if len(route.ContactEdgeVersion) != 1 || route.ContactEdgeVersion[0].Version != 1 {
		t.Fatalf("edge versions = %+v, want one edge v1", route.ContactEdgeVersion)
	}
}

func TestImpactPreviewRemoval(t *testing.T) {
	fixture := newImpactFixture(t)
	preview, err := fixture.service.Preview(context.Background(), fixture.profile.ID, fixture.request([]string{"almond"}))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	route := preview.Routes[0]
	if route.AddedCount != 1 || route.RemovedCount != 1 {
		t.Fatalf("counts = +%d -%d, want +1 -1", route.AddedCount, route.RemovedCount)
	}
	var removed *dto.ImpactCellChange
	for index := range route.Changes {
		if route.Changes[index].ChangeType == "removed" {
			removed = &route.Changes[index]
		}
	}
	if removed == nil || removed.Allergen != "sesame" {
		t.Fatalf("removed change = %+v, want sesame", removed)
	}
	if removed.Before == nil || removed.Before.RiskLevel != constants.RiskMedium {
		t.Fatalf("removed before = %+v, want medium", removed.Before)
	}
	if removed.BeforeEvidence == nil || removed.BeforeEvidence.Allergen != "sesame" {
		t.Fatalf("removed evidence = %+v, want sesame path", removed.BeforeEvidence)
	}
	// Ties between added and removed are ordered by allergen code.
	if preview.GlobalBiggestChange == nil || preview.GlobalBiggestChange.Change.Allergen != "almond" {
		t.Fatalf("global biggest = %+v, want almond tie-break", preview.GlobalBiggestChange)
	}
}

func TestImpactPreviewProfileVersionConflict(t *testing.T) {
	fixture := newImpactFixture(t)
	request := fixture.request([]string{"sesame"})
	request.ExpectedProfileVersion = fixture.profile.Version + 1
	_, err := fixture.service.Preview(context.Background(), fixture.profile.ID, request)
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != "profile_version_conflict" || appErr.Status != 409 {
		t.Fatalf("error = %v, want 409 profile_version_conflict", err)
	}
}

func TestImpactPreviewRouteVersionDriftIsUncalculable(t *testing.T) {
	fixture := newImpactFixture(t)
	request := fixture.request([]string{"sesame"})
	request.ExpectedRouteVersions[fixture.route.RouteCode] = fixture.route.Version + 1
	preview, err := fixture.service.Preview(context.Background(), fixture.profile.ID, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.CalculatedRouteCount != 0 || len(preview.Routes) != 0 {
		t.Fatalf("calculated routes = %d, want 0", preview.CalculatedRouteCount)
	}
	if len(preview.UncalculableRoutes) != 1 {
		t.Fatalf("uncalculable = %+v, want one", preview.UncalculableRoutes)
	}
	entry := preview.UncalculableRoutes[0]
	if entry.ReasonCode != "route_version_conflict" || entry.ExpectedVersion != fixture.route.Version+1 || entry.CurrentVersion != fixture.route.Version {
		t.Fatalf("uncalculable entry = %+v", entry)
	}
}

func TestImpactPreviewUnknownRouteVersionIsUncalculable(t *testing.T) {
	fixture := newImpactFixture(t)
	request := fixture.request([]string{"sesame"})
	request.ExpectedRouteVersions = map[string]uint{"RTE-OTHER": 3}
	preview, err := fixture.service.Preview(context.Background(), fixture.profile.ID, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.UncalculableRoutes) != 1 || preview.UncalculableRoutes[0].ReasonCode != "route_version_unknown" {
		t.Fatalf("uncalculable = %+v, want route_version_unknown", preview.UncalculableRoutes)
	}
}

func TestImpactPreviewCasingOnlyIsNoChange(t *testing.T) {
	fixture := newImpactFixture(t)
	preview, err := fixture.service.Preview(context.Background(), fixture.profile.ID, fixture.request([]string{"SESAME"}))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.AddedAllergens) != 0 || len(preview.RemovedAllergens) != 0 {
		t.Fatalf("allergen set diff = +%v -%v, want empty for case-only change", preview.AddedAllergens, preview.RemovedAllergens)
	}
	route := preview.Routes[0]
	if route.AddedCount != 0 || route.RemovedCount != 0 || route.UpgradedCount != 0 || route.DowngradedCount != 0 || len(route.Changes) != 0 {
		t.Fatalf("route changes = %+v, want no matrix changes for case-only change", route)
	}
	if preview.GlobalBiggestChange != nil {
		t.Fatalf("global biggest change = %+v, want nil", preview.GlobalBiggestChange)
	}
}

func TestImpactPreviewDoesNotPersistAnything(t *testing.T) {
	fixture := newImpactFixture(t)
	if _, err := fixture.service.Preview(context.Background(), fixture.profile.ID, fixture.request([]string{"sesame", "almond"})); err != nil {
		t.Fatalf("preview: %v", err)
	}
	profile, err := fixture.service.profiles.Get(context.Background(), fixture.profile.ID)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if profile.Version != fixture.profile.Version {
		t.Fatalf("profile version = %d, want %d (preview must not bump versions)", profile.Version, fixture.profile.Version)
	}
	if string(profile.AllergensJSON) != string(fixture.profile.AllergensJSON) {
		t.Fatalf("profile allergens changed to %s", profile.AllergensJSON)
	}
}
