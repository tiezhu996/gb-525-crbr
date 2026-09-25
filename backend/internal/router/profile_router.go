package router

import (
	"food-allergen-crosscontact-analyzer/backend/internal/constants"
	"food-allergen-crosscontact-analyzer/backend/internal/handler"
	"food-allergen-crosscontact-analyzer/backend/internal/middleware"
	"github.com/gin-gonic/gin"
)

func registerProfileRoutes(group *gin.RouterGroup, h *handler.ProfileHandler, impacts *handler.ImpactPreviewHandler) {
	profiles := group.Group("/profiles")
	profiles.GET("", h.List)
	profiles.GET("/:id", h.Get)
	profiles.POST("", middleware.RBAC(constants.RoleQualityAnalyst, constants.RoleAdmin), h.Create)
	// Read-only what-if: any authenticated role may preview, matching matrix
	// compute. The endpoint saves nothing and does not stale assessments.
	profiles.POST("/:id/impact-preview", impacts.Preview)
	profiles.PUT("/:id", middleware.RBAC(constants.RoleQualityAnalyst, constants.RoleAdmin), h.Update)
}
