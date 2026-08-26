package controller

import (
	"net/http"

	openapispec "github.com/QuantumNous/new-api/docs/openapi"
	"github.com/gin-gonic/gin"
)

// GetRelayOpenAPISpec serves the public model-gateway contract without
// requiring a dashboard session. The document itself contains no credentials.
func GetRelayOpenAPISpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/json; charset=utf-8", openapispec.RelaySpec())
}
