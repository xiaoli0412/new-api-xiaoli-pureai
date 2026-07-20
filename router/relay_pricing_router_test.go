package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelayPricingContractRouteRequiresAdminManagementAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("relay-pricing-route-test"))))
	SetApiRouter(engine)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=default", nil))

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestRelayPricingContractAcceptsAdminAccessTokenAndRejectsRelayKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open("file:relay_pricing_router_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))

	managementAccessToken := "relay-pricing-management-access-token"
	admin := &model.User{
		Username: "relay-pricing-admin",
		Password: "password",
		Role:     common.RoleAdminUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	admin.SetAccessToken(managementAccessToken)
	require.NoError(t, db.Create(admin).Error)
	require.NoError(t, db.Create(&model.Token{
		UserId:         admin.Id,
		Name:           "ordinary-relay-key",
		Key:            "ordinary-relay-key",
		Status:         common.TokenStatusEnabled,
		CreatedTime:    1,
		AccessedTime:   1,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
		Group:          "default",
	}).Error)

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("relay-pricing-access-token-test"))))
	SetApiRouter(engine)

	managementRequest := httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=default", nil)
	managementRequest.Header.Set("Authorization", "Bearer "+managementAccessToken)
	managementRequest.Header.Set("New-Api-User", strconv.Itoa(admin.Id))
	managementRecorder := httptest.NewRecorder()
	engine.ServeHTTP(managementRecorder, managementRequest)
	require.Equal(t, http.StatusOK, managementRecorder.Code)
	require.Contains(t, managementRecorder.Body.String(), `"success":true`)

	relayRequest := httptest.NewRequest(http.MethodGet, "/api/relay/pricing/v1?group=default", nil)
	relayRequest.Header.Set("Authorization", "Bearer ordinary-relay-key")
	relayRequest.Header.Set("New-Api-User", strconv.Itoa(admin.Id))
	relayRecorder := httptest.NewRecorder()
	engine.ServeHTTP(relayRecorder, relayRequest)
	require.Equal(t, http.StatusOK, relayRecorder.Code)
	require.Contains(t, relayRecorder.Body.String(), `"success":false`)
}
