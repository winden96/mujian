package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func DisableCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		appendVary(c.Writer.Header(), "Cookie", "Authorization")
		c.Next()
	}
}

func appendVary(header http.Header, values ...string) {
	existing := make([]string, 0, len(values)+1)
	seen := make(map[string]struct{}, len(values)+1)
	for _, line := range header.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			existing = append(existing, value)
		}
	}
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, value)
	}
	header.Set("Vary", strings.Join(existing, ", "))
}
