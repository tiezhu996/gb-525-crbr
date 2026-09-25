package dto

type CreateProfileRequest struct {
	ProfileCode           string   `json:"profile_code" validate:"required,min=2,max=64"`
	MaterialName          string   `json:"material_name" validate:"required,min=2,max=180"`
	Allergens             []string `json:"allergens" validate:"required,min=1,dive,required,max=80"`
	SourceType            string   `json:"source_type" validate:"required,oneof=supplier_statement formulation laboratory internal_review"`
	SupplierStatementDate string   `json:"supplier_statement_date" validate:"omitempty,datetime=2006-01-02"`
	ProfileStatus         string   `json:"profile_status" validate:"required,oneof=draft active retired"`
}

type UpdateProfileRequest struct {
	MaterialName          string   `json:"material_name" validate:"required,min=2,max=180"`
	Allergens             []string `json:"allergens" validate:"required,min=1,dive,required,max=80"`
	SourceType            string   `json:"source_type" validate:"required,oneof=supplier_statement formulation laboratory internal_review"`
	SupplierStatementDate string   `json:"supplier_statement_date" validate:"omitempty,datetime=2006-01-02"`
	ProfileStatus         string   `json:"profile_status" validate:"required,oneof=draft active retired"`
	ExpectedVersion       uint     `json:"expected_version" validate:"required,min=1"`
}

type ProfileQuery struct {
	Page     int    `form:"page" validate:"omitempty,min=1"`
	PageSize int    `form:"page_size" validate:"omitempty,min=1,max=100"`
	Search   string `form:"search" validate:"omitempty,max=100"`
	Status   string `form:"status" validate:"omitempty,oneof=draft active retired"`
}

type ProfileUsage struct {
	RouteID      uint   `json:"route_id"`
	RouteCode    string `json:"route_code"`
	ProductName  string `json:"product_name"`
	RouteStatus  string `json:"route_status"`
	RouteVersion uint   `json:"route_version"`
}

type ProfileDetail struct {
	Profile any            `json:"profile"`
	UsedBy  []ProfileUsage `json:"used_by_routes"`
}

// ProfileImpactPreviewRequest is a strictly read-only "what-if" payload: the
// proposed allergen set is propagated against the routes that reference the
// profile without persisting anything. Expected profile/route versions pin
// the calculation to the versions the caller saw when initiating the preview.
type ProfileImpactPreviewRequest struct {
	Allergens       []string          `json:"allergens" validate:"required,min=1,dive,required,max=80"`
	ExpectedVersion uint              `json:"expected_version" validate:"required,min=1"`
	RouteVersions   []RouteVersionPin `json:"route_versions" validate:"dive"`
}

type RouteVersionPin struct {
	RouteID         uint `json:"route_id" validate:"required,min=1"`
	ExpectedVersion uint `json:"expected_version" validate:"required,min=1"`
}
