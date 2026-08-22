package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/gin-gonic/gin"
)

func mujianError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, mujianservice.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, mujianservice.ErrConflict) || errors.Is(err, mujianservice.ErrSessionChanged) {
		status = http.StatusConflict
	}
	if errors.Is(err, mujianservice.ErrRelayUnavailable) {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error()})
}

func ListMujianProjects(c *gin.Context) {
	projects, err := mujianservice.ListProjects(c.GetInt("id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": projects})
}

func CreateMujianProject(c *gin.Context) {
	var request struct {
		Title    string `json:"title"`
		Synopsis string `json:"synopsis"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	project, err := mujianservice.CreateProject(c.GetInt("id"), request.Title, request.Synopsis)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": project})
}

func GetMujianProject(c *gin.Context) {
	project, err := mujianservice.GetProject(c.GetInt("id"), c.Param("projectId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": project})
}

func UpdateMujianProject(c *gin.Context) {
	var request map[string]interface{}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	project, err := mujianservice.UpdateProject(c.GetInt("id"), c.Param("projectId"), request)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": project})
}

func DeleteMujianProject(c *gin.Context) {
	if err := mujianservice.DeleteProject(c.GetInt("id"), c.Param("projectId")); err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func GetMujianWorkspace(c *gin.Context) {
	workspace, err := mujianservice.GetWorkspace(c.GetInt("id"), c.Param("projectId"), c.Query("session_id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": workspace})
}

func CreateMujianAgentSession(c *gin.Context) {
	workspace, err := mujianservice.CreateAgentSession(c.GetInt("id"), c.Param("projectId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": workspace})
}

func ClearMujianAgentSession(c *gin.Context) {
	workspace, err := mujianservice.ClearAgentSession(c.GetInt("id"), c.Param("projectId"), c.Param("sessionId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": workspace})
}

func UpdateMujianWorkspace(c *gin.Context) {
	var request struct {
		Scenes []model.MujianScene `json:"scenes"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	workspace, err := mujianservice.UpdateWorkspace(c.GetInt("id"), c.Param("projectId"), request.Scenes)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": workspace})
}

func SendMujianAgentMessage(c *gin.Context) {
	var request struct {
		Content   string `json:"content"`
		Skill     string `json:"skill"`
		ModelID   string `json:"model_id"`
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	result, err := mujianservice.SendAgentMessage(c.GetInt("id"), c.Param("projectId"), request.Content, request.Skill, request.ModelID, request.SessionID)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func writeMujianSSE(c *gin.Context, event mujianservice.AgentStreamEvent) error {
	data, err := common.Marshal(event.Data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
		return err
	}
	c.Writer.Flush()
	return c.Request.Context().Err()
}

func StreamMujianAgentMessage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, mujianservice.MaxAgentRequestBodyBytes)
	var request struct {
		Content     string                          `json:"content"`
		Skill       string                          `json:"skill"`
		ModelID     string                          `json:"model_id"`
		SessionID   string                          `json:"session_id"`
		Attachments []mujianservice.AgentAttachment `json:"attachments"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			mujianError(c, errors.New("附件请求过大，总大小不能超过 20MB"))
			return
		}
		mujianError(c, err)
		return
	}
	started := false
	emit := func(event mujianservice.AgentStreamEvent) error {
		if !started {
			c.Header("Content-Type", "text/event-stream; charset=utf-8")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")
			c.Status(http.StatusOK)
			started = true
		}
		return writeMujianSSE(c, event)
	}
	err := mujianservice.StreamAgentMessage(
		c.Request.Context(), c.GetInt("id"), c.Param("projectId"),
		request.Content, request.Skill, request.ModelID, request.Attachments, emit, request.SessionID,
	)
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if !started {
		mujianError(c, err)
		return
	}
	common.SysError("mujian agent stream failed: " + err.Error())
	code := "agent_stream_failed"
	if errors.Is(err, mujianservice.ErrSessionChanged) {
		code = "session_changed"
	}
	_ = emit(mujianservice.AgentStreamEvent{Type: "error", Data: gin.H{"code": code, "message": err.Error()}})
}

func ApplyMujianAgentMessage(c *gin.Context) {
	workspace, err := mujianservice.ApplyAgentMessage(c.GetInt("id"), c.Param("projectId"), c.Param("messageId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": workspace})
}

func UndoMujianAgentMessage(c *gin.Context) {
	workspace, err := mujianservice.UndoAgentMessage(c.GetInt("id"), c.Param("projectId"), c.Param("messageId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": workspace})
}

func GenerateMujianShot(c *gin.Context) {
	var request struct {
		ModelID string `json:"model_id"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			mujianError(c, err)
			return
		}
	}
	result, err := mujianservice.GenerateShot(c.GetInt("id"), c.Param("projectId"), c.Param("shotId"), request.ModelID)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": result})
}

func GetMujianImageTask(c *gin.Context) {
	result, err := mujianservice.GetImageTask(c.GetInt("id"), c.Param("projectId"), c.Param("taskId"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func GetMujianPreferences(c *gin.Context) {
	preference, err := mujianservice.GetPreference(c.GetInt("id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": preference, "models": mujianCatalogPayload()})
}

func ListMujianModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": mujianCatalogPayload()})
}

func mujianCatalogPayload() gin.H {
	models := mujianservice.CatalogModels()
	items, err := mujianprovider.CatalogAvailabilityList()
	if err != nil {
		items = []mujianprovider.CatalogAvailability{}
	}
	return gin.H{"chat": models["chat"], "image": models["image"], "items": items}
}

func UpdateMujianPreferences(c *gin.Context) {
	var request struct {
		DefaultChatModel  string `json:"default_chat_model"`
		DefaultImageModel string `json:"default_image_model"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	preference, err := mujianservice.UpdatePreference(c.GetInt("id"), request.DefaultChatModel, request.DefaultImageModel)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": preference})
}

func GetMujianSkills(c *gin.Context) {
	skills, err := mujianservice.GetSkills(c.GetInt("id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": skills})
}

func UpdateMujianSkills(c *gin.Context) {
	var request struct {
		Skills []string `json:"skills"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	skills, err := mujianservice.UpdateSkills(c.GetInt("id"), request.Skills)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": skills})
}

func GetMujianWallet(c *gin.Context) {
	wallet, err := mujianservice.GetWallet(c.GetInt("id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": wallet})
}

func CreateMujianWalletPayment(c *gin.Context) {
	var request struct {
		Credits       int    `json:"credits"`
		PaymentMethod string `json:"payment_method"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	payment, err := mujianservice.CreateWalletPayment(c.Request.Context(), c.GetInt("id"), request.Credits, request.PaymentMethod)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": payment})
}

func WechatPayNotify(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := mujianservice.ProcessWechatPayNotification(c.Request.Context(), c.Request, c.ClientIP()); err != nil {
		common.SysError("wechat pay notification failed: " + err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"code": "FAIL", "message": "支付通知处理失败"})
		return
	}
	c.Status(http.StatusNoContent)
}

func ListMujianWalletOrders(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	orders, total, err := mujianservice.ListWalletOrders(c.GetInt("id"), pageInfo)
	if err != nil {
		mujianError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(orders)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": pageInfo})
}

func GetMujianCherryStudioConfig(c *gin.Context) {
	config, err := mujianservice.GetCherryStudioConfig(c.GetInt("id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": config})
}
