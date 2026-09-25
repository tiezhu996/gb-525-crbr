package handler

import (
	"food-allergen-crosscontact-analyzer/backend/internal/dto"
	"food-allergen-crosscontact-analyzer/backend/internal/service"
	"food-allergen-crosscontact-analyzer/backend/internal/util"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

type ImpactPreviewHandler struct {
	service  *service.ImpactPreviewService
	validate *validator.Validate
}

func NewImpactPreviewHandler(svc *service.ImpactPreviewService, validate *validator.Validate) *ImpactPreviewHandler {
	return &ImpactPreviewHandler{service: svc, validate: validate}
}

// Preview handles the read-only profile change dry run. It never persists the
// proposed allergen set and never creates or invalidates assessments.
func (h *ImpactPreviewHandler) Preview(c *gin.Context) {
	id, ok := util.PathID(c, "id")
	if !ok {
		return
	}
	var request dto.ProfileImpactPreviewRequest
	if !util.BindJSON(c, &request, h.validate) {
		return
	}
	result, err := h.service.Preview(c.Request.Context(), id, request)
	if err != nil {
		util.Error(c, err)
		return
	}
	util.OK(c, result)
}
