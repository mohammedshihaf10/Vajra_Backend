package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Ping godoc
// @Summary Sanity check endpoint
// @Description Returns ok to verify Swagger is scanning handlers
// @Tags health
// @Produce json
// @Success 200 {object} map[string]string
// @Router /ping [get]
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
