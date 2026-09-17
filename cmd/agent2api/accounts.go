package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/app"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// platformDefaultPaths 返回平台各自的默认凭证位置。
func platformDefaultPaths(platform string) []string {
	return workbuddy.DefaultCredentialPaths()
}

// buildAdapter 按配置装配上游：**恒返回号池**（哪怕只有一个账号）。
//
// 单账号也走池的原因：控制台「添加账号」/ 手动放凭证进号池目录后，
// 监视器（poolwatch.go）要能在运行期把新账号 Add 进来 —— 若单账号时
// 返回裸适配器，热加载就得换掉运行期适配器，并发下不安全。
// 单账号走池的调度开销可忽略（Pick 即取 index 0）。
//
// 返回 *adapter.Adapter（即号池）与号池本体（供横幅展示账号明细）。
// 任一账号加载失败只记日志不阻断启动——多账号场景下坏一个文件不该拖死网关；
// 全部失败时才返回错误。
func buildAdapter(cfg config.Config, logger interface{ Printf(string, ...any) }) (adapter.Adapter, *adapter.Pool, error) {
	paths := collectCredentialPaths(cfg)

	logf := func(format string, args ...any) { logger.Printf(format, args...) }
	pool := adapter.NewPool(cfg.Upstream.Platform, logf)
	// 每账号并发上限（0 = 不限）：装配期设定，之后不再变。
	// 这是背压而不是限制功能——请求会排队等槽位，不会因此失败或换号。
	pool.SetMaxConcurrencyPerAccount(cfg.Upstream.MaxConcurrencyPerAccount)
	loaded := 0
	seenUID := map[string]bool{} // 同一账号不得以两份凭证入池：独立刷新链会互相顶掉会话
	for _, p := range paths {
		adp, err := newPlatformAdapter(cfg, p, logf)
		if err != nil {
			// 单账号加载失败不阻断：凭证损坏/过期文件跳过，其余照常入池。
			logf("跳过凭证 %s: %v", filepath.Base(p), err)
			continue
		}
		// 同一账号不得以两份凭证入池：两条独立刷新链会互相顶掉会话
		//（若上游 refresh token 是一次轮换型）。uid 是稳定唯一标识。
		// 用可选接口断言而不是塞进 adapter.Adapter：这是「装配期」的关注点，
		// 不该污染运行期接缝。
		uid := ""
		if u, ok := adp.(interface{ AccountUID() string }); ok {
			uid = u.AccountUID()
		}
		if uid != "" {
			if seenUID[uid] {
				logf("跳过凭证 %s: 与已加载账号重复（uid=%s），同一账号多份凭证会互相顶掉会话",
					filepath.Base(p), uid)
				continue
			}
			seenUID[uid] = true
		}
		pool.Add(accountLabel(p, adp), adp)
		loaded++
	}

	if loaded == 0 {
		return nil, nil, &llm.Failure{
			Code:    "no_credential",
			Message: "未找到任何可用凭证。请先用桌面客户端登录，或运行 agent2api login；多账号请把凭证文件放进 accounts_dir",
		}
	}
	return pool, pool, nil
}

// accountLabel 生成控制台展示用的账号名。
// 桌面客户端自动探测的凭证路径是深层文件名，对人不友好：
// 优先用账号昵称，其次 UID，最后才退回文件名。
// 号池目录里的凭证文件继续用文件名（与磁盘对应，便于管理）。
func accountLabel(path string, adp adapter.Adapter) string {
	for _, p := range platformDefaultPaths("") {
		if p == path {
			name := ""
			if idp, ok := adp.(interface {
				AccountIdentity() workbuddy.AccountIdentity
			}); ok {
				id := idp.AccountIdentity()
				if id.Nickname != "" {
					name = id.Nickname
				} else if id.UID != "" {
					name = id.UID
				}
			}
			if name == "" {
				name = "桌面凭证"
			}
			return name
		}
	}
	return filepath.Base(path)
}

// newPlatformAdapter 按配置的平台构造一个账号适配器。
//
// 平台差异全部收在这个工厂里：号池与下游协议都只依赖 adapter.Adapter 接口，
// 新增平台时只需在这里加一个分支。
func newPlatformAdapter(cfg config.Config, credPath string, logf func(string, ...any)) (adapter.Adapter, error) {
	adp, err := workbuddy.New(workbuddy.Config{
		BaseURL:            cfg.Upstream.BaseURL,
		CredentialPath:     credPath,
		Sanitize:           cfg.Upstream.Sanitize,
		RequestTimeout:     cfg.RequestTimeout(),
		StreamIdleTimeout:  cfg.StreamIdleTimeout(),
		StreamTotalTimeout: cfg.StreamTotalTimeout(),
		ModelCacheTTL:      cfg.ModelCacheTTL(),
		Logger:             logf,
	})
	if err != nil {
		return nil, err
	}
	return adp, nil
}

// collectCredentialPaths 汇总候选凭证文件：
// 显式 CredentialPath 优先，其次 accounts_dir 目录下全部 *.json（按文件名排序保证稳定）。
// 单账号的自动探测路径（桌面客户端凭证等）在 CredentialPath 为空时也参与。
func collectCredentialPaths(cfg config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// 空路径是「交给适配器自行探测」的内部哨兵，**不能**塞给 add()——
	// add 用 p == "" 过滤空值，会把唯一一个自动探测入口直接吃掉，
	// 导致未显式配置凭证时号池空转、网关报 no_credential。
	// 这里改为显式枚举本机默认候选（只收存在的），逐个独立尝试：
	// 第一个文件损坏/过期时还能退到后面的候选。
	if cfg.Upstream.CredentialPath != "" {
		add(cfg.Upstream.CredentialPath)
	} else {
		// 自动探测按平台取默认位置：workbuddy 在 CodeBuddy 的凭证文件里。
		// 新平台的默认位置在各自适配器的 DefaultCredentialPaths 里追加。
		for _, p := range platformDefaultPaths(cfg.Upstream.Platform) {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				add(p)
			}
		}
	}

	// 号池目录：显式配置的优先。
	if dir := cfg.Upstream.AccountsDir; dir != "" {
		addCredentialDir(dir, add)
	}

	// 未配置号池目录时，扫「控制台添加账号的落盘目录」（~/.workbuddy）。
	//
	// 这不是可选的：控制台的「添加账号」固定写这个目录（见 app.DefaultAccountsDir），
	// 而它不在 platformDefaultPaths 的候选里（那里只列了 session.json）。
	// 不扫这里，用户添加的账号就会「写进去了但网关看不见」——实测踩到过。
	if cfg.Upstream.AccountsDir == "" || cfg.Upstream.AccountsDir != app.DefaultAccountsDir() {
		addCredentialDir(app.DefaultAccountsDir(), add)
	}
	return out
}

// addCredentialDir 把目录下的**凭证文件**加入候选（按名排序保证顺序稳定）。
//
// 只收「看起来是凭证」的文件：默认凭证目录（如 ~/.workbuddy）同时也是客户端
// 的数据目录，里面躺着 mcp.json / settings.json / models.json 等几十个无关
// 文件。不加筛选会让启动日志被「跳过凭证 xxx」刷屏，也会白读几十个文件。
//
// 目录不存在不算错误——首次运行时它本来就不存在。
func addCredentialDir(dir string, add func(string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		if !looksLikeCredential(filepath.Join(dir, e.Name())) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	for _, f := range files {
		add(f)
	}
	return
}

// looksLikeCredential 粗判一个 json 文件是否是平台凭证：含非空 accessToken。
//
// 只做浅层结构探测，不追求完整校验（真正的解析交给适配器）；
// 目的是从数据目录里把凭证文件筛出来，其余一律不当候选——
// 默认凭证目录（如 ~/.workbuddy）同时也是客户端数据目录，里面躺着
// mcp.json / settings.json / models.json 等几十个无关文件。
func looksLikeCredential(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return false
	}
	var doc struct {
		Auth struct {
			AccessToken string `json:"accessToken"`
		} `json:"auth"`
		AccessToken string `json:"accessToken"` // 兼容扁平格式
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	return doc.Auth.AccessToken != "" || doc.AccessToken != ""
}

// dedupeCredentialDir 清理**我们自己的**冗余凭证文件。
//
// ⚠️ 安全边界：只处理文件名符合 `account-*.json`（控制台添加账号的产物）
// 的文件，**绝不触碰目录里的其他任何文件**。默认目录 ~/.workbuddy 是
// workbuddy 客户端的数据目录，里面有 mcp.json / settings.json / user-state.json
// 等客户端自己的文件——把它们当「无效凭证」删掉会破坏用户的桌面客户端。
//
// 保留策略：同一 uid 只留最早的一份；无 accessToken 的 account-* 视为
// 登录中途失败的残留，清除。dryRun 时只报告不删除。
func dedupeCredentialDir(dir string, dryRun bool, report func(string, ...any)) (removed int, kept int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		// 只认我们自己写的文件名，其余一律不碰。
		if !strings.HasPrefix(e.Name(), "account-") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files) // 按名排序 ≈ 按写入时间先后

	seenUID := map[string]string{} // uid → 保留的文件路径
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			Auth struct {
				AccessToken string `json:"accessToken"`
			} `json:"auth"`
			Account struct {
				UID string `json:"uid"`
			} `json:"account"`
			AccessToken string `json:"accessToken"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			// 名字像我们的产物但解析不了——保守起见不删，只报告。
			report("跳过（无法解析，请人工确认）: %s", filepath.Base(p))
			continue
		}
		token := doc.Auth.AccessToken
		if token == "" {
			token = doc.AccessToken
		}
		if token == "" {
			report("删除无效凭证（无 accessToken）: %s", filepath.Base(p))
			if !dryRun {
				_ = os.Remove(p)
			}
			removed++
			continue
		}
		uid := doc.Account.UID
		if uid == "" {
			kept++
			continue // 没 uid 无法判重，保守保留
		}
		if first, ok := seenUID[uid]; ok {
			report("删除重复凭证: %s（与 %s 同账号）", filepath.Base(p), filepath.Base(first))
			if !dryRun {
				_ = os.Remove(p)
			}
			removed++
			continue
		}
		seenUID[uid] = p
		kept++
	}
	return removed, kept
}
