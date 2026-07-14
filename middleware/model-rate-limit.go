package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/common/limiter"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

const (
	ModelRequestRateLimitCountMark        = "MRRL"
	ModelRequestRateLimitSuccessCountMark = "MRRLS"
)

// 检查Redis中的请求限制
func checkRedisRateLimit(ctx context.Context, rdb *redis.Client, key string, maxCount int, duration int64) (bool, error) {
	// 如果maxCount为0，表示不限制
	if maxCount == 0 {
		return true, nil
	}

	// 获取当前计数
	length, err := rdb.LLen(ctx, key).Result()
	if err != nil {
		return false, err
	}

	// 如果未达到限制，允许请求
	if length < int64(maxCount) {
		return true, nil
	}

	// 检查时间窗口
	oldTimeStr, _ := rdb.LIndex(ctx, key, -1).Result()
	oldTime, err := time.Parse(timeFormat, oldTimeStr)
	if err != nil {
		return false, err
	}

	nowTimeStr := time.Now().Format(timeFormat)
	nowTime, err := time.Parse(timeFormat, nowTimeStr)
	if err != nil {
		return false, err
	}
	// 如果在时间窗口内已达到限制，拒绝请求
	subTime := nowTime.Sub(oldTime).Seconds()
	if int64(subTime) < duration {
		rdb.Expire(ctx, key, time.Duration(setting.ModelRequestRateLimitDurationMinutes)*time.Minute)
		return false, nil
	}

	return true, nil
}

// 记录Redis请求
func recordRedisRequest(ctx context.Context, rdb *redis.Client, key string, maxCount int) {
	// 如果maxCount为0，不记录请求
	if maxCount == 0 {
		return
	}

	now := time.Now().Format(timeFormat)
	rdb.LPush(ctx, key, now)
	rdb.LTrim(ctx, key, 0, int64(maxCount-1))
	rdb.Expire(ctx, key, time.Duration(setting.ModelRequestRateLimitDurationMinutes)*time.Minute)
}

// Redis限流处理器
func redisRateLimitHandler(duration int64, totalMaxCount, successMaxCount int) gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := strconv.Itoa(c.GetInt("id"))
		ctx := context.Background()
		rdb := common.RDB

		// 1. 检查成功请求数限制
		successKey := fmt.Sprintf("rateLimit:%s:%s", ModelRequestRateLimitSuccessCountMark, userId)
		allowed, err := checkRedisRateLimit(ctx, rdb, successKey, successMaxCount, duration)
		if err != nil {
			fmt.Println("检查成功请求数限制失败:", err.Error())
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
			return
		}
		if !allowed {
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf("您已达到请求数限制：%d分钟内最多请求%d次", setting.ModelRequestRateLimitDurationMinutes, successMaxCount))
			return
		}

		//2.检查总请求数限制并记录总请求（当totalMaxCount为0时会自动跳过，使用令牌桶限流器
		if totalMaxCount > 0 {
			totalKey := fmt.Sprintf("rateLimit:%s", userId)
			// 初始化
			tb := limiter.New(ctx, rdb)
			allowed, err = tb.Allow(
				ctx,
				totalKey,
				limiter.WithCapacity(int64(totalMaxCount)*duration),
				limiter.WithRate(int64(totalMaxCount)),
				limiter.WithRequested(duration),
			)

			if err != nil {
				fmt.Println("检查总请求数限制失败:", err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError, "rate_limit_check_failed")
				return
			}

			if !allowed {
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf("您已达到总请求数限制：%d分钟内最多请求%d次，包括失败次数，请检查您的请求是否正确", setting.ModelRequestRateLimitDurationMinutes, totalMaxCount))
			}
		}

		// 4. 处理请求
		c.Next()

		// 5. 如果请求成功，记录成功请求
		if c.Writer.Status() < 400 {
			recordRedisRequest(ctx, rdb, successKey, successMaxCount)
		}
	}
}

// 内存限流处理器
func memoryRateLimitHandler(duration int64, totalMaxCount, successMaxCount int) gin.HandlerFunc {
	inMemoryRateLimiter.Init(time.Duration(setting.ModelRequestRateLimitDurationMinutes) * time.Minute)

	return func(c *gin.Context) {
		userId := strconv.Itoa(c.GetInt("id"))
		totalKey := ModelRequestRateLimitCountMark + userId
		successKey := ModelRequestRateLimitSuccessCountMark + userId

		// 1. 检查总请求数限制（当totalMaxCount为0时跳过）
		if totalMaxCount > 0 && !inMemoryRateLimiter.Request(totalKey, totalMaxCount, duration) {
			c.Status(http.StatusTooManyRequests)
			c.Abort()
			return
		}

		// 2. 检查成功请求数限制
		// 使用一个临时key来检查限制，这样可以避免实际记录
		checkKey := successKey + "_check"
		if !inMemoryRateLimiter.Request(checkKey, successMaxCount, duration) {
			c.Status(http.StatusTooManyRequests)
			c.Abort()
			return
		}

		// 3. 处理请求
		c.Next()

		// 4. 如果请求成功，记录到实际的成功请求计数中
		if c.Writer.Status() < 400 {
			inMemoryRateLimiter.Request(successKey, successMaxCount, duration)
		}
	}
}

// ModelRequestRateLimit 模型请求限流中间件
func ModelRequestRateLimit() func(c *gin.Context) {
	return func(c *gin.Context) {
		// 先检查用户级别的并发限制
		userConcurrency := common.GetContextKeyInt(c, constant.ContextKeyUserConcurrency)
		if userConcurrency == 0 {
			userConcurrency = setting.DefaultUserConcurrentLimit
		}
		if userConcurrency > 0 {
			if !checkUserConcurrency(c, userConcurrency) {
				return
			}
			// 请求结束后释放并发计数
			defer releaseUserConcurrency(c)
		}

		// 在每个请求时检查是否启用限流
		if !setting.ModelRequestRateLimitEnabled {
			// 即使全局限流未启用，也检查用户级别 RPM
			userRPM := common.GetContextKeyInt(c, constant.ContextKeyUserRateLimit)
			if userRPM == 0 {
				userRPM = setting.DefaultUserRequestRateLimit
			}
			if userRPM > 0 {
				duration := int64(60) // 1分钟
				if common.RedisEnabled {
					redisRateLimitHandler(duration, 0, userRPM)(c)
				} else {
					memoryRateLimitHandler(duration, 0, userRPM)(c)
				}
				return
			}
			c.Next()
			return
		}

		// 计算限流参数
		duration := int64(setting.ModelRequestRateLimitDurationMinutes * 60)
		totalMaxCount := setting.ModelRequestRateLimitCount
		successMaxCount := setting.ModelRequestRateLimitSuccessCount

		// 获取分组
		group := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		if group == "" {
			group = common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		}

		//获取分组的限流配置
		groupTotalCount, groupSuccessCount, found := setting.GetGroupRateLimit(group)
		if found {
			totalMaxCount = groupTotalCount
			successMaxCount = groupSuccessCount
		}

		// 用户级别 RPM 覆盖（优先级最高）
		userRPM := common.GetContextKeyInt(c, constant.ContextKeyUserRateLimit)
		if userRPM == 0 {
			userRPM = setting.DefaultUserRequestRateLimit
		}
		if userRPM > 0 {
			successMaxCount = userRPM
		}

		// 根据存储类型选择并执行限流处理器
		if common.RedisEnabled {
			redisRateLimitHandler(duration, totalMaxCount, successMaxCount)(c)
		} else {
			memoryRateLimitHandler(duration, totalMaxCount, successMaxCount)(c)
		}
	}
}


// checkUserConcurrency 检查用户并发请求数是否超限
// 使用 Redis INCR 或内存计数器实现
func checkUserConcurrency(c *gin.Context, maxConcurrency int) bool {
	userId := strconv.Itoa(c.GetInt("id"))
	key := fmt.Sprintf("concurrency:user:%s", userId)

	if common.RedisEnabled {
		ctx := context.Background()
		rdb := common.RDB
		// 原子递增
		current, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			fmt.Println("检查并发限制失败:", err.Error())
			// 出错时放行
			return true
		}
		// 设置过期时间防止泄漏（5分钟）
		rdb.Expire(ctx, key, 5*time.Minute)
		if current > int64(maxConcurrency) {
			// 超出限制，回退计数
			rdb.Decr(ctx, key)
			abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf("您已达到并发请求限制：最多同时处理%d个请求", maxConcurrency))
			return false
		}
		return true
	}

	// 内存模式：使用 inMemoryConcurrencyLimiter
	inMemoryConcurrencyLimiter.mu.Lock()
	defer inMemoryConcurrencyLimiter.mu.Unlock()
	if inMemoryConcurrencyLimiter.counters == nil {
		inMemoryConcurrencyLimiter.counters = make(map[string]int)
	}
	current := inMemoryConcurrencyLimiter.counters[key]
	if current >= maxConcurrency {
		inMemoryConcurrencyLimiter.mu.Unlock()
		abortWithOpenAiMessage(c, http.StatusTooManyRequests, fmt.Sprintf("您已达到并发请求限制：最多同时处理%d个请求", maxConcurrency))
		inMemoryConcurrencyLimiter.mu.Lock()
		return false
	}
	inMemoryConcurrencyLimiter.counters[key] = current + 1
	return true
}

// releaseUserConcurrency 请求完成后释放并发计数
func releaseUserConcurrency(c *gin.Context) {
	userId := strconv.Itoa(c.GetInt("id"))
	key := fmt.Sprintf("concurrency:user:%s", userId)

	if common.RedisEnabled {
		ctx := context.Background()
		rdb := common.RDB
		result, err := rdb.Decr(ctx, key).Result()
		if err == nil && result < 0 {
			// 防止计数器变为负数
			rdb.Set(ctx, key, 0, 5*time.Minute)
		}
		return
	}

	// 内存模式
	inMemoryConcurrencyLimiter.mu.Lock()
	defer inMemoryConcurrencyLimiter.mu.Unlock()
	if inMemoryConcurrencyLimiter.counters[key] > 0 {
		inMemoryConcurrencyLimiter.counters[key]--
	}
}

// inMemoryConcurrencyLimiter 内存并发计数器
var inMemoryConcurrencyLimiter = struct {
	mu       sync.Mutex
	counters map[string]int
}{}
