package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayOpenAPIEndpointIsPublicJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/openapi/relay.json", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Header().Get("Content-Type"), "application/json")
	var document map[string]any
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &document))
	require.Equal(t, "3.0.3", document["openapi"])
}
