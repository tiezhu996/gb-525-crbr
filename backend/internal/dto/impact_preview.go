package dto

import "food-allergen-crosscontact-analyzer/backend/internal/analyzer"

// ProfileImpactPreviewRequest is a read-only what-if evaluation. Nothing in
// this request is persisted: the allergen profile is not saved and existing
// assessments are never invalidated.
type ProfileImpactPreviewRequest struct {
	// Allergens is the proposed (not yet saved) allergen set.
	Allergens []string `json:"allergens" validate:"required,min=1,max=60,dive,required,max=80"`
	// ExpectedProfileVersion is the profile version the editor was opened on.
	ExpectedProfileVersion uint `json:"expected_profile_version" validate:"required,min=1"`
	// ExpectedRouteVersions maps route_code -> version seen when the preview
	// was initiated. Routes not present in the map cannot be evaluated.
	ExpectedRouteVersions map[string]uint `json:"expected_route_versions" validate:"required,min=1"`
}

// ImpactEdgeVersion identifies the contact edge version used in a computation,
// so the page can show which inputs a route result was derived from.
type ImpactEdgeVersion struct {
	EdgeID  uint `json:"edge_id"`
	Version uint `json:"edge_version"`
	Enabled bool `json:"enabled"`
}

// ImpactCellChange is one target step x allergen matrix cell whose value or
// presence changes between the current profile content and the proposal.
type ImpactCellChange struct {
	ChangeType     string               `json:"change_type"` // added | removed | upgraded | downgraded
	TargetStepCode string               `json:"target_step_code"`
	TargetStepName string               `json:"target_step_name"`
	Allergen       string               `json:"allergen"`
	Before         *analyzer.MatrixCell `json:"before,omitempty"`
	After          *analyzer.MatrixCell `json:"after,omitempty"`
	LevelDelta     int                  `json:"level_delta"`
	ScoreDelta     float64              `json:"score_delta"`
	Declared       bool                 `json:"declared"`
	BeforeEvidence *analyzer.RiskItem   `json:"before_evidence,omitempty"`
	AfterEvidence  *analyzer.RiskItem   `json:"after_evidence,omitempty"`
}

// ImpactRouteResult is the dry-run diff for a single effective route.
type ImpactRouteResult struct {
	RouteID            uint                `json:"route_id"`
	RouteCode          string              `json:"route_code"`
	ProductName        string              `json:"product_name"`
	RouteVersion       uint                `json:"route_version"`
	AddedCount         int                 `json:"added_count"`
	RemovedCount       int                 `json:"removed_count"`
	UpgradedCount      int                 `json:"upgraded_count"`
	DowngradedCount    int                 `json:"downgraded_count"`
	Changes            []ImpactCellChange  `json:"changes"`
	BiggestChange      *ImpactCellChange   `json:"biggest_change,omitempty"`
	ContactEdgeVersion []ImpactEdgeVersion `json:"contact_edge_versions"`
}

// ImpactUncalculableRoute explains why one effective route could not be
// evaluated. Known reason codes:
//   - route_version_conflict: route changed after the preview was initiated
//   - route_version_unknown:  route was not among the versions supplied
//   - profile_missing:        a referenced profile is unavailable
//   - profile_json_invalid:   stored profile content cannot be parsed
//   - graph_invalid:          the route graph (steps/edges) is invalid
//   - propagation_failed:     weighted-path propagation failed
type ImpactUncalculableRoute struct {
	RouteID         uint   `json:"route_id"`
	RouteCode       string `json:"route_code"`
	ProductName     string `json:"product_name"`
	CurrentVersion  uint   `json:"current_version"`
	ExpectedVersion uint   `json:"expected_version"`
	ReasonCode      string `json:"reason_code"`
	ReasonMessage   string `json:"reason_message"`
}

// ProfileImpactPreview is the complete read-only dry run.
type ProfileImpactPreview struct {
	ProfileID            uint                      `json:"profile_id"`
	ProfileCode          string                    `json:"profile_code"`
	ProfileVersion       uint                      `json:"profile_version"`
	CurrentAllergens     []string                  `json:"current_allergens"`
	ProposedAllergens    []string                  `json:"proposed_allergens"`
	AddedAllergens       []string                  `json:"added_allergens"`
	RemovedAllergens     []string                  `json:"removed_allergens"`
	ThresholdVersion     string                    `json:"threshold_version"`
	MaxDepth             int                       `json:"max_depth"`
	EffectiveRouteCount  int                       `json:"effective_route_count"`
	CalculatedRouteCount int                       `json:"calculated_route_count"`
	Routes               []ImpactRouteResult       `json:"routes"`
	UncalculableRoutes   []ImpactUncalculableRoute `json:"uncalculable_routes"`
	GlobalBiggestChange  *GlobalImpactChange       `json:"global_biggest_change,omitempty"`
}

// GlobalImpactChange points at the strongest cell change across every affected
// effective route, carrying the evidence path that explains it.
type GlobalImpactChange struct {
	RouteID      uint             `json:"route_id"`
	RouteCode    string           `json:"route_code"`
	ProductName  string           `json:"product_name"`
	RouteVersion uint             `json:"route_version"`
	Change       ImpactCellChange `json:"change"`
}
