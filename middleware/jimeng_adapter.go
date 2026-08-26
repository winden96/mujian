package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

const (
	jimengSubmitAction = "CVSync2AsyncSubmitTask"
	jimengFetchAction  = "CVSync2AsyncGetResult"
)

func JimengRequestConvert() func(c *gin.Context) {
	return func(c *gin.Context) {
		action := c.Query("Action")
		switch action {
		case jimengSubmitAction, jimengFetchAction:
		case "":
			abortWithOpenAiMessage(c, http.StatusBadRequest, "Action query parameter is required")
			return
		default:
			abortWithOpenAiMessage(c, http.StatusBadRequest, "Unsupported Action query parameter")
			return
		}

		// Handle Jimeng official API request
		var originalReq map[string]interface{}
		if err := common.UnmarshalBodyReusable(c, &originalReq); err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "Invalid request body")
			return
		}
		model, _ := originalReq["req_key"].(string)
		prompt, _ := originalReq["prompt"].(string)

		unifiedReq := map[string]interface{}{
			"model":    model,
			"prompt":   prompt,
			"metadata": originalReq,
		}

		jsonData, err := json.Marshal(unifiedReq)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "Failed to marshal request body")
			return
		}

		// Update request body
		c.Request.Body = io.NopCloser(bytes.NewBuffer(jsonData))
		c.Set(common.KeyRequestBody, jsonData)

		if image, ok := originalReq["image"]; !ok || image == "" {
			c.Set("action", constant.TaskActionTextGenerate)
		}

		c.Request.URL.Path = "/v1/video/generations"

		if action == jimengFetchAction {
			taskId, ok := originalReq["task_id"].(string)
			if !ok || taskId == "" {
				abortWithOpenAiMessage(c, http.StatusBadRequest, "task_id is required for "+jimengFetchAction)
				return
			}
			c.Request.URL.Path = "/v1/video/generations/" + taskId
			c.Request.Method = http.MethodGet
			c.Set("task_id", taskId)
			c.Set("relay_mode", relayconstant.RelayModeVideoFetchByID)
		}
		c.Next()
	}
}
