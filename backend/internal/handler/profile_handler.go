package handler

import (
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/service"
	"food-allergen-crosscontact-analyzer/backend/internal/util"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

type ProfileHandler struct {
	service  *service.ProfileService
	impact   *service.ProfileImpactService
	validate *validator.Validate
}

func NewProfileHandler(svc *service.ProfileService, impact *service.ProfileImpactService, validate *validator.Validate) *ProfileHandler {
	return &ProfileHandler{service: svc, impact: impact, validate: validate}
}

func (h *ProfileHandler) List(c *gin.Context) {
	var query dto.ProfileQuery
	if !util.BindQuery(c, &query, h.validate) {
		return
	}
	items, total, err := h.service.List(c.Request.Context(), query)
	if err != nil {
		util.Error(c, err)
		return
	}
	page, size := util.PageValues(query.Page, query.PageSize)
	util.Page(c, items, page, size, total)
}

func (h *ProfileHandler) Get(c *gin.Context) {
	id, ok := util.PathID(c, "id")
	if !ok {
		return
	}
	item, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		util.Error(c, err)
		return
	}
	util.OK(c, item)
}

func (h *ProfileHandler) Create(c *gin.Context) {
	principal, ok := util.Principal(c)
	if !ok {
		return
	}
	var request dto.CreateProfileRequest
	if !util.BindJSON(c, &request, h.validate) {
		return
	}
	item, err := h.service.Create(c.Request.Context(), request, principal, RequestID(c))
	if err != nil {
		util.Error(c, err)
		return
	}
	util.Created(c, item)
}

func (h *ProfileHandler) Update(c *gin.Context) {
	id, ok := util.PathID(c, "id")
	if !ok {
		return
	}
	principal, ok := util.Principal(c)
	if !ok {
		return
	}
	var request dto.UpdateProfileRequest
	if !util.BindJSON(c, &request, h.validate) {
		return
	}
	item, err := h.service.Update(c.Request.Context(), id, request, principal, RequestID(c))
	if err != nil {
		util.Error(c, err)
		return
	}
	util.OK(c, item)
}

// ImpactPreview is read-only: it computes the matrix diff of a proposed
// allergen set against the active routes referencing the profile but never
// persists the profile, routes, edges, or assessment runs.
func (h *ProfileHandler) ImpactPreview(c *gin.Context) {
	id, ok := util.PathID(c, "id")
	if !ok {
		return
	}
	var request dto.ProfileImpactPreviewRequest
	if !util.BindJSON(c, &request, h.validate) {
		return
	}
	result, err := h.impact.Preview(c.Request.Context(), id, request)
	if err != nil {
		util.Error(c, err)
		return
	}
	util.OK(c, result)
}
