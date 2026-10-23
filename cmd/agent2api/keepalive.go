package main

import (
	"context"
	"log"
	"time"

	"github.com/576469377/Agent2API/internal/app"
)

// 号池凭证保活。
//
// 解决的问题：token 只在**账号被选中时**才刷新（EnsureValid 在拨号前调用）。
// 于是一个长期不被轮询到的账号，token 会一直过期下去；等哪天轮到它，
// 才发现 refreshToken 也可能已经过期 —— 用户看到的是「这个号怎么突然失效了」，
// 而它本可以在后台悄悄保持可用。
//
// 机制：每 keepaliveInterval 遍历所有账号调 EnsureValid(ctx)。
// EnsureValid 内部只在「即将过期」时才真正刷新，不会白打上游；
// 且自带锁，与并发请求安全共存。

// keepaliveInterval 是保活检查周期。
// 凭证有效期是小时级，15 分钟足够早；又不至于频繁打扰上游。
const keepaliveInterval = 15 * time.Minute

// startKeepalive 启动保活 goroutine。
func startKeepalive(ctx context.Context, hub *app.Hub, logger *log.Logger) {
	go runPeriodic(ctx, keepaliveInterval, func() { refreshExpiring(ctx, hub, logger) })
}

// refreshExpiring 让所有「凭证即将过期」的账号刷新一次。
func refreshExpiring(ctx context.Context, hub *app.Hub, logger *log.Logger) {
	for _, rt := range hub.Platforms() {
		pool := rt.Pool
		if pool == nil {
			continue
		}
		for _, st := range pool.Statuses() {
			if ctx.Err() != nil {
				return
			}
			if st.Adapter == nil {
				continue
			}
			// 用可选接口：只有能自查凭证有效性的适配器才参与保活。
			v, ok := st.Adapter.(interface {
				EnsureValid(context.Context) error
			})
			if !ok {
				continue
			}
			if err := v.EnsureValid(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				// 单个账号失败后继续检查其余账号。
				logger.Printf("凭证保活检查失败（账号 %s）: %v", st.Label, err)
				continue
			}
			logger.Printf("凭证保活检查完成: %s", st.Label)
		}
	}
}
