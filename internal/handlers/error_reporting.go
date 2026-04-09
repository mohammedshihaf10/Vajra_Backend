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

func respondWithExactError(c *gin.Context, statusCode int, err error, fallback string) {
	if err == nil {
		c.JSON(statusCode, gin.H{"error": fallback})
		return
	}

	_ = c.Error(err)
	c.JSON(statusCode, gin.H{"error": err.Error()})
}

func respondWithInternalError(c *gin.Context, publicMessage string, err error) {
	respondWithError(c, http.StatusInternalServerError, publicMessage, err)
}
