package main

// 号池热加载：运行期自动发现新凭证并加入轮询。
//
// 解决的问题：控制台「添加账号」或手动把凭证放进号池目录后，
// 旧实现要**重启网关**才能生效——用户在控制台里添加了账号，
// 账号页却还是只有旧账号，体验是断裂的。
//
// 机制：每 10 秒扫描各平台的号池目录（未配置时兜底 ~/.workbuddy，
// 与控制台「添加账号」的落盘位置一致），发现新凭证就构造适配器
// 加进运行中的号池。按 UID 去重，同一账号的多份凭证不会重复入池。

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/app"
	"github.com/576469377/Agent2API/internal/config"
	"log"
)

// poolWatchInterval 是号池目录的扫描周期。
const poolWatchInterval = 10 * time.Second

// startPoolWatchers 为每个平台启动一个目录监视 goroutine。
// 平台未配置 accounts_dir 时监视兜底目录（~/.workbuddy），
// 保证控制台添加的凭证总能被捡到。
func startPoolWatchers(hub *app.Hub, cfg config.Config, logger *log.Logger) {
	for _, rt := range hub.Platforms() {
		dir := rt.AccountsDir()
		if dir == "" {
			dir = app.DefaultAccountsDir()
		}
		go watchAccountDir(rt, dir, cfg, logger)
	}
}

// watchAccountDir 周期扫描 dir，把新出现的凭证加入运行中的号池。
// 失败的文件每个周期重试（可能是写到一半被抓到），但同一种错误只记一次日志。
func watchAccountDir(rt *app.PlatformRuntime, dir string, cfg config.Config, logger *log.Logger) {
	logf := func(format string, args ...any) { logger.Printf(format, args...) }
	lastErr := map[string]string{}

	for range time.Tick(poolWatchInterval) {
		pool := rt.Pool
		if pool == nil {
			continue // 理论不可达：buildAdapter 恒返回号池
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // 目录还没建：等控制台添加账号时创建
		}

		// 现有账号的 UID 集合：同一账号多份凭证只入池一次。
		existingUID := map[string]bool{}
		existingLabel := map[string]bool{}
		for _, st := range rt.AccountStatuses() {
			existingLabel[st.Label] = true
			if u, ok := st.Adapter.(interface{ AccountUID() string }); ok {
				if uid := u.AccountUID(); uid != "" {
					existingUID[uid] = true
				}
			}
		}

		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".json") {
				continue
			}
			if existingLabel[name] {
				continue
			}
			path := filepath.Join(dir, name)

			adp, err := workbuddy.New(workbuddy.Config{
				CredentialPath:     path,
				Sanitize:           cfg.Upstream.Sanitize,
				RequestTimeout:     cfg.RequestTimeout(),
				StreamIdleTimeout:  cfg.StreamIdleTimeout(),
				StreamTotalTimeout: cfg.StreamTotalTimeout(),
				ModelCacheTTL:      cfg.ModelCacheTTL(),
				Logger:             logf,
			})
			if err != nil {
				// 凭证无效（损坏/缺字段/或与现有账号同一 UID）——静默重试，
				// 同一错误只记一次日志，避免每 10 秒刷屏。
				if lastErr[name] != err.Error() {
					lastErr[name] = err.Error()
					// 同一文件同一错误只记一次：默认目录里躺着几十个
					// 非凭证文件，每 10s 刷一遍是日志灾难。
					if lastErr[name] != err.Error() {
						lastErr[name] = err.Error()
						logf("号池目录候选 %s 暂不可入池: %v", name, err)
					}
				}
				continue
			}
			// workbuddy.New 返回具体类型，AccountUID 是它的方法，直接调用。
			uid := adp.AccountUID()
			if uid != "" && existingUID[uid] {
				// 与已有账号同一身份（如桌面凭证的导出副本）——不入池不报错。
				lastErr[name] = "duplicate"
				continue
			}
			if uid != "" {
				existingUID[uid] = true
			}
			existingLabel[name] = true
			pool.Add(name, adp)
			delete(lastErr, name)
			logf("新账号已入池: %s（%s）", name, dir)
		}
	}
}
