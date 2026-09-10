package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetOptionsHidesMujianDefaultModelInternals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{
		"RetryTimes":                                        "1",
		"_mujian_default_chat_model_auto:gate":              "open:00000000-0000-0000-0000-000000000001",
		"_mujian_default_chat_model_auto:sonnet-4-6:00":     `{"user_ids":[1]}`,
		"_mujian_default_model_active_cutover":              "sensitive-state",
		"_mujian_default_model_commit:operation-identifier": "sensitive-state",
		"_mujian_provider_lock:zenmux":                      "sensitive-state",
		"_mujian_provider_generation:zenmux":                "sensitive-state",
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	GetOptions(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    []model.Option `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	for _, option := range response.Data {
		require.False(t, isMujianInternalOption(option.Key), option.Key)
	}
}

func TestUpdateOptionRejectsEveryMujianDefaultModelInternalKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	keys := []string{
		"_mujian_default_chat_model_auto:gate",
		"_mujian_default_chat_model_auto:sonnet-4-6:00",
		"_mujian_default_model_active_cutover",
		"_mujian_default_model_commit:operation-identifier",
		"_mujian_provider_lock:zenmux",
		"_mujian_provider_generation:zenmux",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			body, err := common.Marshal(OptionUpdateRequest{Key: key, Value: "tampered"})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", bytes.NewReader(body))

			UpdateOption(context)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "内部选项不可通过公共接口修改")
		})
	}
}
