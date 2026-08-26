package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func authTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("auth-test-secret"))))
	router.GET("/seed/:id/:role", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		role, _ := strconv.Atoi(c.Param("role"))
		session := sessions.Default(c)
		session.Set("id", id)
		session.Set("username", "session-user")
		session.Set("role", role)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	router.GET("/user", UserAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/admin", AdminAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/token-or-user", TokenOrUserAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.GetInt("id"), "role": c.GetInt("role")})
	})
	return router
}

func sessionCookie(t *testing.T, router *gin.Engine, path string) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.NotEmpty(t, recorder.Result().Cookies())
	return recorder.Result().Cookies()[0]
}

func TestUserAuthRejectsSessionForDeletedUser(t *testing.T) {
	router := authTestRouter(t)
	cookie := sessionCookie(t, router, "/seed/99/1")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/user", nil)
	request.AddCookie(cookie)
	request.Header.Set("New-Api-User", "99")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestAdminAuthUsesLiveRoleInsteadOfSessionRole(t *testing.T) {
	router := authTestRouter(t)
	user := model.User{Username: "live-user", Password: "hashed-password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default"}
	require.NoError(t, model.DB.Create(&user).Error)
	cookie := sessionCookie(t, router, "/seed/"+strconv.Itoa(user.Id)+"/100")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.AddCookie(cookie)
	request.Header.Set("New-Api-User", strconv.Itoa(user.Id))
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "success")
}

func TestTokenOrUserAuthPropagatesSessionIdentity(t *testing.T) {
	router := authTestRouter(t)
	require.NoError(t, model.DB.Create(&model.User{
		Id:       7,
		Username: "token-or-user",
		Password: "hashed-password",
		Role:     common.RoleAdminUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}).Error)
	cookie := sessionCookie(t, router, "/seed/7/10")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/token-or-user", nil)
	request.AddCookie(cookie)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":7,"role":10}`, recorder.Body.String())
}

func TestTokenOrUserAuthUsesCurrentRoleInsteadOfSessionRole(t *testing.T) {
	router := authTestRouter(t)
	require.NoError(t, model.DB.Create(&model.User{
		Id:       8,
		Username: "demoted-user",
		Password: "hashed-password",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}).Error)
	cookie := sessionCookie(t, router, "/seed/8/100")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/token-or-user", nil)
	request.AddCookie(cookie)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":8,"role":1}`, recorder.Body.String())
}
