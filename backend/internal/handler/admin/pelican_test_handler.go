package admin

import (
	"errors"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// PelicanTestHandler exposes admin-only group-level SVG batch probes.
type PelicanTestHandler struct {
	pelicanTestSvc *service.PelicanTestService
}

func NewPelicanTestHandler(pelicanTestSvc *service.PelicanTestService) *PelicanTestHandler {
	return &PelicanTestHandler{pelicanTestSvc: pelicanTestSvc}
}

// ListRuns GET /admin/pelican-tests/runs
func (h *PelicanTestHandler) ListRuns(c *gin.Context) {
	var groupID *int64
	if raw := c.Query("group_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "invalid group id")
			return
		}
		groupID = &id
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	runs, err := h.pelicanTestSvc.ListRuns(c.Request.Context(), groupID, limit)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, runs)
}

// CreateRun POST /admin/pelican-tests/runs
func (h *PelicanTestHandler) CreateRun(c *gin.Context) {
	var req service.CreatePelicanTestRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	adminID := int64(0)
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		adminID = subject.UserID
	}
	run, err := h.pelicanTestSvc.StartRun(c.Request.Context(), req, adminID)
	if err != nil {
		if errors.Is(err, service.ErrPelicanTestRunInProgress) {
			response.ErrorFrom(c, err)
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, run)
}

// GetRun GET /admin/pelican-tests/runs/:id
func (h *PelicanTestHandler) GetRun(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid run id")
		return
	}
	run, err := h.pelicanTestSvc.GetRun(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "pelican test run not found")
		return
	}
	response.Success(c, run)
}

// DeleteRun DELETE /admin/pelican-tests/runs/:id
func (h *PelicanTestHandler) DeleteRun(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid run id")
		return
	}
	if err := h.pelicanTestSvc.DeleteRun(c.Request.Context(), id); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
