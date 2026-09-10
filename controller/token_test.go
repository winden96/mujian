package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type tokenAPIResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type tokenPageResponse struct {
	Items []tokenResponseItem `json:"items"`
}

type tokenResponseItem struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Key    string `json:"key"`
	Status int    `json:"status"`
}

type tokenKeyResponse struct {
	Key string `json:"key"`
}

func setupTokenControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	model.DB = db
	model.LOG_DB = db

	if err := db.AutoMigrate(&model.Token{}); err != nil {
		t.Fatalf("failed to migrate token table: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func seedToken(t *testing.T, db *gorm.DB, userID int, name string, rawKey string) *model.Token {
	t.Helper()

	token := &model.Token{
		UserId:         userID,
		Name:           name,
		Key:            rawKey,
		Status:         common.TokenStatusEnabled,
		CreatedTime:    1,
		AccessedTime:   1,
		ExpiredTime:    -1,
		RemainQuota:    100,
		UnlimitedQuota: true,
		Group:          "default",
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("failed to create token: %v", err)
	}
	return token
}

func newAuthenticatedContext(t *testing.T, method string, target string, body any, userID int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	var requestBody *bytes.Reader
	if body != nil {
		payload, err := common.Marshal(body)
		if err != nil {
			t.Fatalf("failed to marshal request body: %v", err)
		}
		requestBody = bytes.NewReader(payload)
	} else {
		requestBody = bytes.NewReader(nil)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, requestBody)
	if body != nil {
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	ctx.Set("id", userID)
	return ctx, recorder
}

func decodeAPIResponse(t *testing.T, recorder *httptest.ResponseRecorder) tokenAPIResponse {
	t.Helper()

	var response tokenAPIResponse
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode api response: %v", err)
	}
	return response
}

func TestGetAllTokensMasksKeyInResponse(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedToken(t, db, 1, "list-token", "abcd1234efgh5678") // gitleaks:allow -- deterministic test fixture
	seedToken(t, db, 2, "other-user-token", "zzzz1234yyyy5678")

	ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/?p=1&size=10", nil, 1)
	GetAllTokens(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("expected success response, got message: %s", response.Message)
	}

	var page tokenPageResponse
	if err := common.Unmarshal(response.Data, &page); err != nil {
		t.Fatalf("failed to decode token page response: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected exactly one token, got %d", len(page.Items))
	}
	if page.Items[0].Key != token.GetMaskedKey() {
		t.Fatalf("expected masked key %q, got %q", token.GetMaskedKey(), page.Items[0].Key)
	}
	if strings.Contains(recorder.Body.String(), token.Key) {
		t.Fatalf("list response leaked raw token key: %s", recorder.Body.String())
	}
}

func TestSearchTokensMasksKeyInResponse(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedToken(t, db, 1, "searchable-token", "ijkl1234mnop5678") // gitleaks:allow -- deterministic test fixture

	ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/search?keyword=searchable-token&p=1&size=10", nil, 1)
	SearchTokens(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("expected success response, got message: %s", response.Message)
	}

	var page tokenPageResponse
	if err := common.Unmarshal(response.Data, &page); err != nil {
		t.Fatalf("failed to decode search response: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected exactly one search result, got %d", len(page.Items))
	}
	if page.Items[0].Key != token.GetMaskedKey() {
		t.Fatalf("expected masked search key %q, got %q", token.GetMaskedKey(), page.Items[0].Key)
	}
	if strings.Contains(recorder.Body.String(), token.Key) {
		t.Fatalf("search response leaked raw token key: %s", recorder.Body.String())
	}
}

func TestGetTokenMasksKeyInResponse(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedToken(t, db, 1, "detail-token", "qrst1234uvwx5678") // gitleaks:allow -- deterministic test fixture

	ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/"+strconv.Itoa(token.Id), nil, 1)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(token.Id)}}
	GetToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("expected success response, got message: %s", response.Message)
	}

	var detail tokenResponseItem
	if err := common.Unmarshal(response.Data, &detail); err != nil {
		t.Fatalf("failed to decode token detail response: %v", err)
	}
	if detail.Key != token.GetMaskedKey() {
		t.Fatalf("expected masked detail key %q, got %q", token.GetMaskedKey(), detail.Key)
	}
	if strings.Contains(recorder.Body.String(), token.Key) {
		t.Fatalf("detail response leaked raw token key: %s", recorder.Body.String())
	}
}

func TestUpdateTokenMasksKeyInResponse(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedToken(t, db, 1, "editable-token", "yzab1234cdef5678") // gitleaks:allow -- deterministic test fixture

	body := map[string]any{
		"id":                   token.Id,
		"name":                 "updated-token",
		"expired_time":         -1,
		"remain_quota":         100,
		"unlimited_quota":      true,
		"model_limits_enabled": false,
		"model_limits":         "",
		"group":                "default",
		"cross_group_retry":    false,
	}

	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/token/", body, 1)
	UpdateToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("expected success response, got message: %s", response.Message)
	}

	var detail tokenResponseItem
	if err := common.Unmarshal(response.Data, &detail); err != nil {
		t.Fatalf("failed to decode token update response: %v", err)
	}
	if detail.Key != token.GetMaskedKey() {
		t.Fatalf("expected masked update key %q, got %q", token.GetMaskedKey(), detail.Key)
	}
	if strings.Contains(recorder.Body.String(), token.Key) {
		t.Fatalf("update response leaked raw token key: %s", recorder.Body.String())
	}
}

func TestTokenHandlersRejectMujianReservedName(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", map[string]any{
		"name": "  " + model.MujianInternalTokenName + "  ", "unlimited_quota": true,
	}, 1)
	AddToken(ctx)
	response := decodeAPIResponse(t, recorder)
	if response.Success || !strings.Contains(response.Message, "保留") {
		t.Fatalf("expected reserved-name create rejection, got %#v", response)
	}
	var count int64
	if err := db.Model(&model.Token{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("reserved token was persisted: count=%d err=%v", count, err)
	}

	token := seedToken(t, db, 1, "ordinary", "reservedhandlerfixture") // gitleaks:allow -- deterministic test fixture
	ctx, recorder = newAuthenticatedContext(t, http.MethodPut, "/api/token/?basic_only=true", map[string]any{
		"id": token.Id, "name": model.MujianInternalTokenName, "status": common.TokenStatusEnabled,
	}, 1)
	UpdateToken(ctx)
	response = decodeAPIResponse(t, recorder)
	if response.Success || !strings.Contains(response.Message, "保留") {
		t.Fatalf("expected reserved-name update rejection, got %#v", response)
	}
	var stored model.Token
	if err := db.First(&stored, token.Id).Error; err != nil {
		t.Fatalf("failed to reload ordinary token: %v", err)
	}
	if stored.Name != "ordinary" {
		t.Fatalf("reserved name was written: %q", stored.Name)
	}
}

func TestBasicTokenUpdatePreservesRestrictions(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	allowIps := "192.0.2.10\n198.51.100.20"
	token := seedToken(t, db, 1, "restricted-token", "pres1234erve5678") // gitleaks:allow -- deterministic test fixture
	token.ExpiredTime = 2_000_000_000
	token.RemainQuota = 321
	token.UnlimitedQuota = false
	token.ModelLimitsEnabled = true
	token.ModelLimits = "gpt-4o,claude-3-5-sonnet"
	token.AllowIps = &allowIps
	token.Group = "auto"
	token.CrossGroupRetry = true
	if err := db.Save(token).Error; err != nil {
		t.Fatalf("failed to restrict token: %v", err)
	}

	body := map[string]any{
		"id":     token.Id,
		"name":   "renamed-token",
		"status": common.TokenStatusDisabled,
	}
	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/token/?basic_only=true", body, 1)
	UpdateToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("expected success response, got message: %s", response.Message)
	}
	var detail tokenResponseItem
	if err := common.Unmarshal(response.Data, &detail); err != nil {
		t.Fatalf("failed to decode token update response: %v", err)
	}
	if detail.Key != token.GetMaskedKey() || strings.Contains(recorder.Body.String(), token.Key) {
		t.Fatalf("basic update response did not mask the token key")
	}

	updated, err := model.GetTokenByIds(token.Id, 1)
	if err != nil {
		t.Fatalf("failed to reload token: %v", err)
	}
	if updated.Name != "renamed-token" || updated.Status != common.TokenStatusDisabled {
		t.Fatalf("basic fields were not updated: name=%q status=%d", updated.Name, updated.Status)
	}
	if updated.Key != token.Key {
		t.Fatalf("token key changed during a basic update")
	}
	if updated.ExpiredTime != token.ExpiredTime ||
		updated.RemainQuota != token.RemainQuota ||
		updated.UnlimitedQuota != token.UnlimitedQuota ||
		updated.ModelLimitsEnabled != token.ModelLimitsEnabled ||
		updated.ModelLimits != token.ModelLimits ||
		updated.Group != token.Group ||
		updated.CrossGroupRetry != token.CrossGroupRetry {
		t.Fatalf("token restrictions changed during a basic update: %#v", updated)
	}
	if updated.AllowIps == nil || *updated.AllowIps != allowIps {
		t.Fatalf("IP restrictions changed during a basic update: %v", updated.AllowIps)
	}
}

func TestStatusOnlyTokenUpdatePreservesOtherFields(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	allowIps := "203.0.113.10"
	token := seedToken(t, db, 1, "status-token", "stat1234only5678") // gitleaks:allow -- deterministic test fixture
	token.ExpiredTime = 2_000_000_000
	token.RemainQuota = 654
	token.UnlimitedQuota = false
	token.ModelLimitsEnabled = true
	token.ModelLimits = "gpt-4o"
	token.AllowIps = &allowIps
	token.Group = "auto"
	token.CrossGroupRetry = true
	if err := db.Save(token).Error; err != nil {
		t.Fatalf("failed to restrict token: %v", err)
	}

	body := map[string]any{
		"id":     token.Id,
		"status": common.TokenStatusDisabled,
	}
	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/token/?status_only=true", body, 1)
	UpdateToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("expected success response, got message: %s", response.Message)
	}
	updated, err := model.GetTokenByIds(token.Id, 1)
	if err != nil {
		t.Fatalf("failed to reload token: %v", err)
	}
	if updated.Status != common.TokenStatusDisabled {
		t.Fatalf("status was not updated: %d", updated.Status)
	}
	if updated.Name != token.Name ||
		updated.Key != token.Key ||
		updated.ExpiredTime != token.ExpiredTime ||
		updated.RemainQuota != token.RemainQuota ||
		updated.UnlimitedQuota != token.UnlimitedQuota ||
		updated.ModelLimitsEnabled != token.ModelLimitsEnabled ||
		updated.ModelLimits != token.ModelLimits ||
		updated.Group != token.Group ||
		updated.CrossGroupRetry != token.CrossGroupRetry ||
		updated.AllowIps == nil || *updated.AllowIps != allowIps {
		t.Fatalf("non-status fields changed during a status-only update")
	}
}

func TestGetTokenKeyRequiresOwnershipAndReturnsFullKey(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token := seedToken(t, db, 1, "owned-token", "owner1234token5678")

	authorizedCtx, authorizedRecorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/"+strconv.Itoa(token.Id)+"/key", nil, 1)
	authorizedCtx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(token.Id)}}
	GetTokenKey(authorizedCtx)

	authorizedResponse := decodeAPIResponse(t, authorizedRecorder)
	if !authorizedResponse.Success {
		t.Fatalf("expected authorized key fetch to succeed, got message: %s", authorizedResponse.Message)
	}

	var keyData tokenKeyResponse
	if err := common.Unmarshal(authorizedResponse.Data, &keyData); err != nil {
		t.Fatalf("failed to decode token key response: %v", err)
	}
	if keyData.Key != token.GetFullKey() {
		t.Fatalf("expected full key %q, got %q", token.GetFullKey(), keyData.Key)
	}

	unauthorizedCtx, unauthorizedRecorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/"+strconv.Itoa(token.Id)+"/key", nil, 2)
	unauthorizedCtx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(token.Id)}}
	GetTokenKey(unauthorizedCtx)

	unauthorizedResponse := decodeAPIResponse(t, unauthorizedRecorder)
	if unauthorizedResponse.Success {
		t.Fatalf("expected unauthorized key fetch to fail")
	}
	if strings.Contains(unauthorizedRecorder.Body.String(), token.Key) {
		t.Fatalf("unauthorized key response leaked raw token key: %s", unauthorizedRecorder.Body.String())
	}
}
