package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAetherEventsRouteRequiresServiceAuth(t *testing.T) {
	previousDB := model.DB
	previousClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	testID := strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	db, err := gorm.Open(sqlite.Open("file:aether_sync_router_test_"+testID+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	common.RDB = nil
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RDB = previousClient
		common.RedisEnabled = previousRedisEnabled
	})
	require.NoError(t, db.AutoMigrate(&model.AetherIntegration{}, &model.AetherLedgerEvent{}))
	integration := &model.AetherIntegration{ChannelID: 99, InstanceID: "aether-primary", ExecutionMode: model.AetherExecutionModeDirectChannel, Enabled: true, ConfigRevision: 1}
	require.NoError(t, integration.SetSecrets("control-secret", "relay-signing-secret"))
	require.NoError(t, db.Create(integration).Error)
	require.NoError(t, db.Create(&model.AetherLedgerEvent{InstanceID: "aether-primary", EventType: model.AetherLedgerEventUsageSettled, DedupeKey: "event-1", OccurredAt: 1, Payload: `{}`, CreatedTime: 1}).Error)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)
	request := httptest.NewRequest(http.MethodGet, "/api/aether/v1/events?after=0", nil)
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	nonce := "router-nonce-" + testID
	canonical := request.Method + "\n" + request.URL.EscapedPath() + "\n" + request.URL.RawQuery + "\n" + timestamp + "\n" + nonce
	request.Header.Set("X-Aether-Instance-ID", "aether-primary")
	request.Header.Set("X-Aether-Timestamp", timestamp)
	request.Header.Set("X-Aether-Nonce", nonce)
	request.Header.Set("X-Aether-Signature", common.GenerateHMACWithKey([]byte("control-secret"), canonical))
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"events"`)
}
