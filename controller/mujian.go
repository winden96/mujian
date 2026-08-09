package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/gin-gonic/gin"
)

func mujianError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, mujianservice.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, mujianservice.ErrConflict) {
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
	workspace, err := mujianservice.GetWorkspace(c.GetInt("id"), c.Param("projectId"))
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
		Content string `json:"content"`
		Skill   string `json:"skill"`
		ModelID string `json:"model_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	result, err := mujianservice.SendAgentMessage(c.GetInt("id"), c.Param("projectId"), request.Content, request.Skill, request.ModelID)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
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
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func GetMujianPreferences(c *gin.Context) {
	preference, err := mujianservice.GetPreference(c.GetInt("id"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": preference, "models": mujianservice.CatalogModels()})
}

func ListMujianModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": mujianservice.CatalogModels()})
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

func RechargeMujianDemoWallet(c *gin.Context) {
	var request struct {
		Credits int `json:"credits"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	wallet, err := mujianservice.DemoRecharge(c.GetInt("id"), request.Credits)
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": wallet, "message": "演示充值成功，不发起真实支付"})
}
