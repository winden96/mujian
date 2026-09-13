package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This fixture uses the production router, handlers, session auth and database
// migrations. It never connects to a configured deployment or payment provider.
func adminRechargeRouter(t *testing.T) *gin.Engine {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousSQLite, previousMySQL, previousPostgres := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	previousRedis, previousSetup, previousMaster, previousPath := common.RedisEnabled, constant.Setup, common.IsMasterNode, common.SQLitePath
	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "claude-opus-5")
	t.Setenv("MUJIAN_CHAT_MODELS", "claude-opus-5")
	common.SQLitePath = filepath.Join(t.TempDir(), "integration.db") + "?_pragma=busy_timeout(10000)"
	common.IsMasterNode, common.RedisEnabled, constant.Setup = true, false, true
	// InitDB also initializes dialect-specific quoted column names used by the
	// existing account/billing pages; schema migration alone is insufficient.
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = previousSQLite, previousMySQL, previousPostgres
		common.RedisEnabled, constant.Setup, common.IsMasterNode, common.SQLitePath = previousRedis, previousSetup, previousMaster, previousPath
		_ = sqlDB.Close()
	})
	require.NoError(t, i18n.Init())
	password, err := common.Password2Hash("recharge-fixture-password")
	require.NoError(t, err)
	for index, role := range []int{common.RoleAdminUser, common.RoleCommonUser, common.RoleRootUser} {
		name := []string{"recharge-admin", "recharge-recipient", "recharge-root"}[index]
		user := model.User{Id: index + 1, Username: name, DisplayName: name, Password: password, Role: role, Status: common.UserStatusEnabled, Group: "default", AffCode: name}
		require.NoError(t, db.Create(&user).Error)
	}
	engine := gin.New()
	engine.Use(gin.Recovery(), sessions.Sessions("recharge-test", cookie.NewStore([]byte("isolated-recharge-cookie-secret"))))
	SetApiRouter(engine)
	return engine
}

func rechargeHTTP(t *testing.T, client *http.Client, method, endpoint, body string, userID int) (int, map[string]interface{}) {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if userID > 0 {
		request.Header.Set("New-Api-User", strconv.Itoa(userID))
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	var result map[string]interface{}
	require.NoError(t, common.DecodeJson(response.Body, &result))
	return response.StatusCode, result
}

func TestAdminRechargeAuthenticatedHTTPFlow(t *testing.T) {
	server := httptest.NewServer(adminRechargeRouter(t))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}
	request := fmt.Sprintf(`{"credits":1000000,"remark":"合同补充积分","request_id":%q}`, uuid.NewString())
	status, _ := rechargeHTTP(t, client, "POST", server.URL+"/api/user/2/recharge", request, 1)
	require.Equal(t, http.StatusUnauthorized, status)
	_, login := rechargeHTTP(t, client, "POST", server.URL+"/api/user/login", `{"username":"recharge-admin","password":"recharge-fixture-password"}`, 0)
	require.Equal(t, true, login["success"])
	for _, invalid := range []string{`{"credits":1.5}`, `{"credits":"100"}`, `{"credits":null}`, `{"credits":0}`, `{"credits":1000001}`} {
		status, _ = rechargeHTTP(t, client, "POST", server.URL+"/api/user/2/recharge", invalid, 1)
		require.Equal(t, http.StatusBadRequest, status)
	}
	status, _ = rechargeHTTP(t, client, "POST", server.URL+"/api/user/3/recharge", request, 1)
	require.Equal(t, http.StatusForbidden, status)
	for i := 0; i < 2; i++ {
		status, result := rechargeHTTP(t, client, "POST", server.URL+"/api/user/2/recharge", request, 1)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, true, result["success"])
	}
	_, history := rechargeHTTP(t, client, "GET", server.URL+"/api/user/topup", "", 1)
	require.Equal(t, true, history["success"])
	require.EqualValues(t, 1, history["data"].(map[string]interface{})["total"])
	_, login = rechargeHTTP(t, client, "POST", server.URL+"/api/user/login", `{"username":"recharge-recipient","password":"recharge-fixture-password"}`, 0)
	require.Equal(t, true, login["success"])
	_, wallet := rechargeHTTP(t, client, "GET", server.URL+"/api/mujian/wallet", "", 2)
	require.Equal(t, true, wallet["success"])
	require.InDelta(t, 1000000, wallet["data"].(map[string]interface{})["credits"], 0.001)
	_, orders := rechargeHTTP(t, client, "GET", server.URL+"/api/mujian/wallet/orders", "", 2)
	require.Equal(t, true, orders["success"])
	_, forbidden := rechargeHTTP(t, client, "POST", server.URL+"/api/user/1/recharge", request, 2)
	require.Equal(t, false, forbidden["success"])
}

func TestAdminRechargeBrowserFixture(t *testing.T) {
	if os.Getenv("MUJIAN_RECHARGE_BROWSER_FIXTURE") != "1" {
		t.Skip("opt-in local browser fixture")
	}
	engine := adminRechargeRouter(t)
	dist, err := filepath.Abs("../web/dist")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dist, "index.html"))
	engine.Static("/assets", filepath.Join(dist, "assets"))
	engine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Status(404)
			return
		}
		c.File(filepath.Join(dist, "index.html"))
	})
	finished := make(chan struct{})
	engine.POST("/__recharge_fixture/finish", func(c *gin.Context) {
		c.Status(204)
		select {
		case <-finished:
		default:
			close(finished)
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:4188")
	require.NoError(t, err)
	server := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	t.Log("isolated recharge browser fixture ready at http://127.0.0.1:4188")
	select {
	case <-finished:
	case <-time.After(15 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
}
