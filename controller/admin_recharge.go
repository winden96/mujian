package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/gin-gonic/gin"
)

func AdminRechargeUser(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	var request mujianservice.AdminRechargeRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err != nil || userID <= 0 || c.ShouldBindJSON(&request) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": mujianservice.ErrAdminRechargeInput.Error()})
		return
	}
	result, err := mujianservice.RechargeUserCredits(c.Request.Context(), c.GetInt("id"), userID, request)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
		return
	}
	status := http.StatusInternalServerError
	message := "充值处理失败，请使用原请求重试"
	switch {
	case errors.Is(err, mujianservice.ErrAdminRechargeInput):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, model.ErrAdminRechargePermission):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, model.ErrAdminRechargeUser):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, model.ErrAdminRechargeConflict):
		status, message = http.StatusConflict, err.Error()
	default:
		common.SysError("admin recharge failed: " + err.Error())
	}
	c.JSON(status, gin.H{"success": false, "message": message})
}
