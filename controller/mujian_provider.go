package controller

import (
	"fmt"
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

func ImportMujianYuYuPricing(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var request mujianprovider.YuYuPricingImport
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, fmt.Errorf("价格文件不是有效 JSON 或超过 2 MB"))
		return
	}
	if err := mujianprovider.ImportYuYuPricing(request); err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "羽宇价格已导入，请同步模型并测试后启用"})
}

func ConfigureMujianProvider(c *gin.Context) {
	var request struct {
		Key   string  `json:"key"`
		Group *string `json:"group"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	if err := mujianprovider.ConfigureFromAPI(c.Param("provider"), request.Key, request.Group); err != nil {
		mujianError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "供应商密钥已保存"})
}

func PatchMujianProvider(c *gin.Context) {
	var request struct {
		Enabled *bool   `json:"enabled"`
		Group   *string `json:"group"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		mujianError(c, err)
		return
	}
	hasEnabled := request.Enabled != nil
	hasGroup := request.Group != nil
	if hasEnabled == hasGroup {
		mujianError(c, fmt.Errorf("每次只能修改 enabled 或 group 其中一项"))
		return
	}
	var err error
	if request.Group != nil {
		err = mujianprovider.SetGroup(c.Param("provider"), *request.Group)
	} else {
		err = mujianprovider.SetEnabled(c.Param("provider"), *request.Enabled)
	}
	if err != nil {
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
