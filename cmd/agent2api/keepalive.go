package main

import (
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
// 机制：每 keepaliveInterval 遍历所有账号调 EnsureValid()。
// EnsureValid 内部只在「即将过期」时才真正刷新，不会白打上游；
// 且自带锁，与并发请求安全共存。

// keepaliveInterval 是保活检查周期。
// 凭证有效期是小时级，15 分钟足够早；又不至于频繁打扰上游。
const keepaliveInterval = 15 * time.Minute

// startKeepalive 启动保活 goroutine。
func startKeepalive(hub *app.Hub, logger *log.Logger) {
	go func() {
		// 先等一个周期：启动瞬间不需要保活（凭证刚被 buildAdapter 验证过）。
		time.Sleep(keepaliveInterval)
		for range time.Tick(keepaliveInterval) {
			refreshExpiring(hub, logger)
		}
	}()
}

// refreshExpiring 让所有「凭证即将过期」的账号刷新一次。
func refreshExpiring(hub *app.Hub, logger *log.Logger) {
	for _, rt := range hub.Platforms() {
		pool := rt.Pool
		if pool == nil {
			continue
		}
		for _, st := range pool.Statuses() {
			if st.Adapter == nil {
				continue
			}
			// 用可选接口：只有能自查凭证有效性的适配器才参与保活。
			v, ok := st.Adapter.(interface {
				EnsureValid() error
			})
			if !ok {
				continue
			}
			if err := v.EnsureValid(); err != nil {
				// 刷新失败不阻断：凭证可能仍可用（旧 token 未必失效）。
				logger.Printf("保活刷新失败（账号 %s，不影响使用）: %v", st.Label, err)
				continue
			}
			logger.Printf("已保活刷新凭证: %s", st.Label)
		}
	}
}
