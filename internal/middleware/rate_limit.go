package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type rateEntry struct {
	count     int
	windowEnd time.Time
}

func RateLimit(limit int, window time.Duration) gin.HandlerFunc {
	if limit <= 0 {
		limit = 30
	}
	if window <= 0 {
		window = time.Minute
	}

	var (
		mu      sync.Mutex
		entries = map[string]rateEntry{}
	)

	return func(c *gin.Context) {
		key := c.ClientIP()
		if host, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil && host != "" {
			key = host
		}

		now := time.Now()
		mu.Lock()
		entry := entries[key]
		if now.After(entry.windowEnd) {
			entry = rateEntry{windowEnd: now.Add(window)}
		}
		entry.count++
		entries[key] = entry
		mu.Unlock()

		if entry.count > limit {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}
