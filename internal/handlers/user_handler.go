package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"vijraBackend/internal/models"
	"vijraBackend/internal/repositories"
)

type UserHandler struct {
	repo *repositories.UserRepository
}

func NewUserHandler(db *sqlx.DB) *UserHandler {
	return &UserHandler{
		repo: repositories.NewUserRepository(db),
	}
}

// CreateUser godoc
// @Summary Create a new user
// @Description Create a user with name and email
// @Tags users
// @Accept json
// @Produce json
// @Param user body models.User true "User payload"
// @Success 201 {object} models.User
// @Failure 400 {object} map[string]string
// @Router /users/create [post]
func (h *UserHandler) CreateUser(c *gin.Context) {
	var user models.User

	if err := c.ShouldBindJSON(&user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := h.repo.Create(&user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "could not create user",
		})
		return
	}

	c.JSON(http.StatusCreated, user)
}

// GetUserByEmail godoc
// @Summary Get a user by email
// @Description Fetch a user using their email address
// @Tags users
// @Produce json
// @Param email path string true "User email"
// @Success 200 {object} models.User
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /users/{email} [get]
// GET /users/:email
func (h *UserHandler) GetUserByEmail(c *gin.Context) {
	email := c.Param("email")

	user, err := h.repo.GetByEmail(email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to fetch user",
		})
		return
	}

	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "user not found",
		})
		return
	}

	c.JSON(http.StatusOK, user)
}

// GetUser godoc
// @Summary Get a user by ID
// @Description Fetch a user using their numeric ID
// @Tags users
// @Produce json
// @Param id path int true "User ID"
// @Success 200 {object} models.User
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /users/{id} [get]
// GET /users/:id
func (h *UserHandler) GetUser(c *gin.Context) {
	idParam := c.Param("id")
	id, err := strconv.Atoi(idParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid user id",
		})
		return
	}

	user, err := h.repo.GetByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to fetch user",
		})
		return
	}

	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "user not found",
		})
		return
	}

	c.JSON(http.StatusOK, user)
}
