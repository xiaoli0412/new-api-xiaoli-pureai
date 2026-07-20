package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const (
	AetherInstanceIDHeader        = "X-Aether-Instance-ID"
	AetherTimestampHeader         = "X-Aether-Timestamp"
	AetherNonceHeader             = "X-Aether-Nonce"
	AetherSignatureHeader         = "X-Aether-Signature"
	AetherInstanceIDContextKey    = "aether_instance_id"
	AetherIntegrationIDContextKey = "aether_integration_id"
	aetherServiceAuthMaxAge       = 5 * time.Minute
	aetherServiceAuthMaxNonceSize = 256
	aetherNonceRedisKeyPrefix     = "new-api:aether:service-nonce:"
)

var aetherServiceNonces sync.Map // map[string]time.Time

func AetherServiceAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		instanceID := strings.TrimSpace(c.GetHeader(AetherInstanceIDHeader))
		timestamp := strings.TrimSpace(c.GetHeader(AetherTimestampHeader))
		nonce := strings.TrimSpace(c.GetHeader(AetherNonceHeader))
		signature := strings.TrimSpace(c.GetHeader(AetherSignatureHeader))
		if instanceID == "" || timestamp == "" || len(nonce) < 12 || len(nonce) > aetherServiceAuthMaxNonceSize || signature == "" {
			abortAetherServiceAuth(c)
			return
		}
		timestampValue, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || time.Since(time.Unix(timestampValue, 0)).Abs() > aetherServiceAuthMaxAge {
			abortAetherServiceAuth(c)
			return
		}
		integration, err := model.GetAetherIntegrationByInstanceID(instanceID)
		if err != nil || !integration.Enabled {
			abortAetherServiceAuth(c)
			return
		}
		controlSecrets, err := integration.ActiveControlSecrets(time.Now().UTC())
		if err != nil {
			common.SysError("failed to read aether control secrets: " + err.Error())
			abortAetherServiceAuth(c)
			return
		}
		canonical := c.Request.Method + "\n" + c.Request.URL.EscapedPath() + "\n" + c.Request.URL.RawQuery + "\n" + timestamp + "\n" + nonce
		provided, err := hex.DecodeString(signature)
		if err != nil {
			abortAetherServiceAuth(c)
			return
		}
		validSignature := false
		for _, controlSecret := range controlSecrets {
			expected, err := hex.DecodeString(common.GenerateHMACWithKey([]byte(controlSecret), canonical))
			if err == nil && hmac.Equal(expected, provided) {
				validSignature = true
				break
			}
		}
		if !validSignature {
			abortAetherServiceAuth(c)
			return
		}
		nonceKey := instanceID + ":" + nonce
		if !rememberAetherServiceNonce(nonceKey, time.Unix(timestampValue, 0).Add(aetherServiceAuthMaxAge)) {
			abortAetherServiceAuth(c)
			return
		}
		c.Set(AetherInstanceIDContextKey, instanceID)
		c.Set(AetherIntegrationIDContextKey, integration.Id)
		c.Next()
	}
}

func rememberAetherServiceNonce(key string, expiresAt time.Time) bool {
	now := time.Now()
	if !expiresAt.After(now) {
		return false
	}
	if common.RedisEnabled {
		if common.RDB == nil {
			common.SysError("failed to store aether service nonce: Redis is enabled but unavailable")
			return false
		}
		digest := sha256.Sum256([]byte(key))
		stored, err := common.RDB.SetNX(
			context.Background(),
			aetherNonceRedisKeyPrefix+hex.EncodeToString(digest[:]),
			"1",
			expiresAt.Sub(now),
		).Result()
		if err == nil {
			return stored
		}
		common.SysError("failed to store aether service nonce in Redis: " + err.Error())
		return false
	}
	aetherServiceNonces.Range(func(key any, value any) bool {
		expires, ok := value.(time.Time)
		if !ok || !expires.After(now) {
			aetherServiceNonces.Delete(key)
		}
		return true
	})
	_, loaded := aetherServiceNonces.LoadOrStore(key, expiresAt)
	return !loaded
}

func abortAetherServiceAuth(c *gin.Context) {
	c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "invalid aether service authentication"})
	c.Abort()
}
