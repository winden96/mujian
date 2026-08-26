package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/" || strings.HasSuffix(path, ".html") || path == "/logo.png" || path == "/favicon.ico" || path == "/mujian-mark.png" || path == "/mujian-mark.svg" {
			c.Header("Cache-Control", "no-cache")
		} else {
			c.Header("Cache-Control", "max-age=604800") // one week
		}
		c.Header("Cache-Version", "mujian-mark-mj-yellow-20260827")
		c.Next()
	}
}
