package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"food-allergen-crosscontact-analyzer/backend/internal/analyzer"
	"food-allergen-crosscontact-analyzer/backend/internal/config"
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/model"
	"food-allergen-crosscontact-analyzer/backend/internal/repository"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type impactFixture struct {
	svc       *ProfileImpactService
	db        *gorm.DB
	profileID uint
	routeID   uint
}

func openTestDB(t *testing.T) *repository.Database {
	t.Helper()
	database, err := repository.Open(config.Config{DBDriver: "sqlite", DBDSN: "file:impact-test?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func newImpactFixture(t *testing.T, steps []dto.RouteStep, edgeRecords []model.ContactEdge) impactFixture {
	t.Helper()
	database := openTestDB(t)
	profileRepo := repository.NewProfileRepository(database.DB)
	routeRepo := repository.NewRouteRepository(database.DB)
	edgeRepo := repository.NewContactEdgeRepository(database.DB)
	cfg := config.Config{MaxPropagationDepth: 12, Thresholds: config.Thresholds{Medium: .12, High: .35, Critical: .65, Version: "test-v1"}, JWTSecret: "test-secret-at-least-32-bytes-long"}
	svc, err := NewProfileImpactService(routeRepo, profileRepo, edgeRepo, cfg)
	if err != nil {
		t.Fatal(err)
	}

	target := model.AllergenProfile{ProfileCode: "P-TARGET", MaterialName: "Target Material", AllergensJSON: jsonAllergenArray(t, []string{"Milk"}), SourceType: "supplier_statement", ProfileStatus: "active", Version: 1, CreatedBy: 1}
	plain := model.AllergenProfile{ProfileCode: "P-PLAIN", MaterialName: "Plain Material", AllergensJSON: jsonAllergenArray(t, []string{"Water"}), SourceType: "formulation", ProfileStatus: "active", Version: 1, CreatedBy: 1}
	if err := database.DB.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&plain).Error; err != nil {
		t.Fatal(err)
	}
	stepsJSON, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	route := model.ProcessRoute{RouteCode: "R-1", ProductName: "Product One", OrderedStepsJSON: datatypes.JSON(stepsJSON), DeclaredAllergensJSON: jsonAllergenArray(t, []string{}), RouteStatus: "active", Version: 1, OwnerID: 1}
	if err := database.DB.Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	for index := range edgeRecords {
		edgeRecords[index].RouteID = route.ID
		if err := database.DB.Create(&edgeRecords[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return impactFixture{svc: svc, db: database.DB, profileID: target.ID, routeID: route.ID}
}

func jsonAllergenArray(t *testing.T, values []string) datatypes.JSON {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return datatypes.JSON(raw)
}

func assertAppError(t *testing.T, err error, status int, code string) *AppError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	app, ok := err.(*AppError)
	if !ok {
		t.Fatalf("expected *AppError, got %T: %v", err, err)
	}
	if app.Status != status || app.Code != code {
		t.Fatalf("error = %d %s, want %d %s", app.Status, app.Code, status, code)
	}
	return app
}

func TestImpactPreviewVersionConflicts(t *testing.T) {
	steps := []dto.RouteStep{{StepCode: "A", StepName: "Source", ProfileID: 1}, {StepCode: "B", StepName: "Target", ProfileID: 2}}
	edges := []model.ContactEdge{{FromStepCode: "A", ToStepCode: "B", ContactType: "shared_line", SharedEquipment: "Line 1", CleaningFactor: .5, CarryoverProbability: .8, EvidenceNote: "evidence", Enabled: true}}
	fixture := newImpactFixture(t, steps, edges)
	ctx := context.Background()

	t.Run("stale profile version", func(t *testing.T) {
		_, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 99, RouteVersions: []dto.RouteVersionPin{{RouteID: fixture.routeID, ExpectedVersion: 1}}})
		assertAppError(t, err, http.StatusConflict, "profile_version_conflict")
	})

	t.Run("missing route pin", func(t *testing.T) {
		_, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 1})
		assertAppError(t, err, http.StatusConflict, "route_version_conflict")
	})

	t.Run("stale route version", func(t *testing.T) {
		_, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 1, RouteVersions: []dto.RouteVersionPin{{RouteID: fixture.routeID, ExpectedVersion: 42}}})
		appErr := assertAppError(t, err, http.StatusConflict, "route_version_conflict")
		conflicts, ok := appErr.Details.([]RouteVersionConflict)
		if !ok || len(conflicts) != 1 || conflicts[0].Reason != "route_version_conflict" {
			t.Fatalf("unexpected conflict details: %#v", appErr.Details)
		}
	})
}

func TestImpactPreviewAddsAndRemovesCells(t *testing.T) {
	// A (target profile) -> B (plain profile), edge weight .4.
	steps := []dto.RouteStep{{StepCode: "A", StepName: "Source", ProfileID: 1}, {StepCode: "B", StepName: "Target", ProfileID: 2}}
	edges := []model.ContactEdge{{FromStepCode: "A", ToStepCode: "B", ContactType: "shared_line", SharedEquipment: "Line 1", CleaningFactor: .5, CarryoverProbability: .8, EvidenceNote: "evidence", Enabled: true}}
	fixture := newImpactFixture(t, steps, edges)
	ctx := context.Background()
	pins := []dto.RouteVersionPin{{RouteID: fixture.routeID, ExpectedVersion: 1}}

	t.Run("adds allergen cell with evidence path", func(t *testing.T) {
		preview, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 1, RouteVersions: pins})
		if err != nil {
			t.Fatal(err)
		}
		if preview.ActiveRouteCount != 1 || preview.ComputedRouteCount != 1 || preview.FailedRouteCount != 0 {
			t.Fatalf("route counts = %+v", preview)
		}
		route := preview.Routes[0]
		if route.ChangeCounts["added"] != 1 {
			t.Fatalf("added = %v, want 1 (%#v)", route.ChangeCounts, route.Changes)
		}
		if preview.AllergenDelta.Added[0] != "Egg" || len(preview.AllergenDelta.Removed) != 0 {
			t.Fatalf("allergen delta = %#v", preview.AllergenDelta)
		}
		if preview.BiggestChange == nil || preview.BiggestChange.Kind != analyzer.ChangeAdded || preview.BiggestChange.Allergen != "Egg" {
			t.Fatalf("biggest change = %#v", preview.BiggestChange)
		}
		if preview.BiggestChange.AfterEvidence == nil || len(preview.BiggestChange.AfterEvidence.Path) != 2 {
			t.Fatalf("after evidence path missing: %#v", preview.BiggestChange.AfterEvidence)
		}
		if preview.BiggestChangeRouteCode != "R-1" {
			t.Fatalf("biggest change route = %q", preview.BiggestChangeRouteCode)
		}
	})

	t.Run("removes allergen cell", func(t *testing.T) {
		preview, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Egg"}, ExpectedVersion: 1, RouteVersions: pins})
		if err != nil {
			t.Fatal(err)
		}
		route := preview.Routes[0]
		if route.ChangeCounts["removed"] != 1 || route.ChangeCounts["added"] != 1 {
			t.Fatalf("counts = %#v", route.ChangeCounts)
		}
		var removed analyzer.CellChange
		for _, change := range route.Changes {
			if change.Kind == analyzer.ChangeRemoved {
				removed = change
			}
		}
		if removed.BeforeEvidence == nil || removed.BeforeEvidence.RawScore != .4 {
			t.Fatalf("removed evidence = %#v", removed.BeforeEvidence)
		}
	})
}

func TestImpactPreviewUpgradeAcrossBands(t *testing.T) {
	// Both A (target) and B carry Milk into C. B->C is strong (.81), A->C is
	// weak (.02). Target also adds Egg weakly; the largest change must be an
	// added Egg cell ranked by its own movement, and Milk at C stays dominated
	// by B's strong path after the target drops Milk.
	steps := []dto.RouteStep{
		{StepCode: "A", StepName: "Source One", ProfileID: 1},
		{StepCode: "B", StepName: "Source Two", ProfileID: 3},
		{StepCode: "C", StepName: "Target", ProfileID: 2},
	}
	edges := []model.ContactEdge{
		{FromStepCode: "A", ToStepCode: "C", ContactType: "shared_line", SharedEquipment: "Line 1", CleaningFactor: .9, CarryoverProbability: .2, EvidenceNote: "weak edge", Enabled: true},
		{FromStepCode: "B", ToStepCode: "C", ContactType: "shared_tank", SharedEquipment: "Tank 1", CleaningFactor: .1, CarryoverProbability: .9, EvidenceNote: "strong edge", Enabled: true},
	}
	fixture := newImpactFixture(t, steps, edges)
	otherMilk := model.AllergenProfile{ProfileCode: "P-OTHER-MILK", MaterialName: "Other Milk", AllergensJSON: jsonAllergenArray(t, []string{"Milk"}), SourceType: "formulation", ProfileStatus: "active", Version: 1, CreatedBy: 1}
	if err := fixture.db.Create(&otherMilk).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pins := []dto.RouteVersionPin{{RouteID: fixture.routeID, ExpectedVersion: 1}}

	// Adding Egg: a new cell at C with weak raw score .02.
	preview, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 1, RouteVersions: pins})
	if err != nil {
		t.Fatal(err)
	}
	route := preview.Routes[0]
	if !route.Computed {
		t.Fatalf("route not computed: %s %s", route.ErrorCode, route.ErrorMessage)
	}
	if route.ChangeCounts["added"] != 1 || route.ChangeCounts["removed"] != 0 {
		t.Fatalf("counts = %#v changes = %#v", route.ChangeCounts, route.Changes)
	}
	if preview.BiggestChange == nil || preview.BiggestChange.Allergen != "Egg" {
		t.Fatalf("biggest change = %#v", preview.BiggestChange)
	}

	// Dropping Milk from the weak source: C's Milk cell remains (strong B->C),
	// so no removed/downgraded Milk cell appears.
	dropPreview, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Egg"}, ExpectedVersion: 1, RouteVersions: pins})
	if err != nil {
		t.Fatal(err)
	}
	dropRoute := dropPreview.Routes[0]
	if dropRoute.ChangeCounts["added"] != 1 || dropRoute.ChangeCounts["removed"] != 0 {
		t.Fatalf("counts = %#v changes = %#v", dropRoute.ChangeCounts, dropRoute.Changes)
	}
}

func TestImpactPreviewUncomputableRouteReportsReason(t *testing.T) {
	fixture := newImpactFixture(t, []dto.RouteStep{{StepCode: "A", StepName: "Source", ProfileID: 1}}, nil)
	ctx := context.Background()
	preview, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 1, RouteVersions: []dto.RouteVersionPin{{RouteID: fixture.routeID, ExpectedVersion: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ComputedRouteCount != 0 || preview.FailedRouteCount != 1 {
		t.Fatalf("counts = %+v", preview)
	}
	row := preview.Routes[0]
	if row.Computed || row.ErrorCode == "" || row.ErrorMessage == "" {
		t.Fatalf("expected explicit uncomputable reason, got %#v", row)
	}
}

func TestImpactPreviewSkipsDraftRoutes(t *testing.T) {
	steps := []dto.RouteStep{{StepCode: "A", StepName: "Source", ProfileID: 1}, {StepCode: "B", StepName: "Target", ProfileID: 2}}
	fixture := newImpactFixture(t, steps, nil)
	if err := fixture.db.Model(&model.ProcessRoute{}).Where("id = ?", fixture.routeID).Update("route_status", "draft").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// No route pins required because no active route references the profile.
	preview, err := fixture.svc.Preview(ctx, fixture.profileID, dto.ProfileImpactPreviewRequest{Allergens: []string{"Milk", "Egg"}, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ActiveRouteCount != 0 || preview.FailedRouteCount != 0 {
		t.Fatalf("active routes = %d failed = %d", preview.ActiveRouteCount, preview.FailedRouteCount)
	}
}
