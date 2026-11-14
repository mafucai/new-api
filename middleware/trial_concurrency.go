package middleware

import (
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

// TrialConcurrencyLimit is account-scoped, not token- or IP-scoped. It holds
// the slot until the handler returns, including the entire SSE/WebSocket stream.
// This is for the current single-engine deployment; multiple replicas must use
// a shared limiter before they can provide the same account-wide guarantee.
func TrialConcurrencyLimit() gin.HandlerFunc {
	var active sync.Map
	return func(c *gin.Context) {
		if c.GetString(string(constant.ContextKeyUserGroup)) != "trial" {
			c.Next()
			return
		}
		userID := c.GetInt("id")
		if userID <= 0 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		if _, busy := active.LoadOrStore(userID, struct{}{}); busy {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"message": "Trial accounts allow only one active request. Wait for the current request to finish.",
					"type": "rate_limit_error",
					"code": "trial_concurrency_limit",
				},
			})
			return
		}
		defer active.Delete(userID)
		c.Next()
	}
}
