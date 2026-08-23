package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/gin-gonic/gin"
)

func ListMujianProviders(c *gin.Context) {
	providers, err := mujianprovider.Statuses()
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": providers})
}

func ConfigureMujianProvider(c *gin.Context) {
	var request struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	if err := mujianprovider.Configure(c.Param("provider"), request.Key); err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "供应商密钥已保存"})
}

func SetMujianProviderEnabled(c *gin.Context) {
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	if err := mujianprovider.SetEnabled(c.Param("provider"), request.Enabled); err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func TestMujianProvider(c *gin.Context) {
	modelCount, latency, err := mujianprovider.Test(c.Param("provider"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"model_count": modelCount, "latency_ms": latency}})
}

func SyncMujianProvider(c *gin.Context) {
	modelCount, err := mujianprovider.Sync(c.Param("provider"))
	if err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"model_count": modelCount}})
}
