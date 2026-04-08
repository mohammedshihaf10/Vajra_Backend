package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

func respondWithError(c *gin.Context, statusCode int, publicMessage string, err error) {
	if err != nil {
		_ = c.Error(fmt.Errorf("%s: %w", publicMessage, err))
	}
	c.JSON(statusCode, gin.H{"error": publicMessage})
}

func respondBadRequest(c *gin.Context, err error) {
	if err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
}
