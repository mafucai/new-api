package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const (
	signupRiskWindow          = 30 * time.Minute
	signupRiskMaxRegistrations = 3
	trialRiskMaxUsers          = 3
)

const signupRiskContextKey = "trial_signup_risk_reason"

// SignupRiskCheck counts successful human-verified registration attempts by
// IP. It never rejects registration or bans the account; the controller uses
// the reason to revoke only the free trial subscription after creation.
func SignupRiskCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.SignupRiskEnabled {
			c.Next()
			return
		}
		if common.RedisEnabled && common.RDB != nil {
			key := fmt.Sprintf("trialRisk:signup:%s", c.ClientIP())
			count, err := common.RDB.Incr(context.Background(), key).Result()
			if err == nil {
				if count == 1 {
					common.RDB.Expire(context.Background(), key, signupRiskWindow)
				}
				if count > signupRiskMaxRegistrations {
					c.Set(signupRiskContextKey, "同一公网 IP 在30分钟内注册超过3个账号")
				}
			}
		}
		c.Next()
	}
}

// TrialCallRisk tracks distinct trial accounts calling from one IP. Once the
// threshold is reached, only the current trial subscription is suspended;
// the account itself remains usable for paid access.
func TrialCallRisk() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.SignupRiskEnabled || c.GetString("user_group") != "trial" || !common.RedisEnabled || common.RDB == nil {
			c.Next()
			return
		}
		key := fmt.Sprintf("trialRisk:call:%s", c.ClientIP())
		member := fmt.Sprintf("%d", c.GetInt("id"))
		ctx := context.Background()
		if err := common.RDB.SAdd(ctx, key, member).Err(); err == nil {
			common.RDB.Expire(ctx, key, signupRiskWindow)
			if count, err := common.RDB.SCard(ctx, key).Result(); err == nil && count > trialRiskMaxUsers {
				model.SuspendTrialSubscription(c.GetInt("id"), "同一公网 IP 在30分钟内集中调用超过3个体验账号")
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
					"message": "Trial access is temporarily paused for this network. Paid access is unaffected.",
					"type": "trial_risk_limit",
					"code": "trial_risk_limit",
				}})
				return
			}
		}
		c.Next()
	}
}

func SignupRiskReason(c *gin.Context) string {
	return c.GetString(signupRiskContextKey)
}
