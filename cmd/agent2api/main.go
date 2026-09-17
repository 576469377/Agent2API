// Command agent2api 是把 AI Agent 平台私有协议转换为标准 LLM API 的反向代理网关。
//
// 用法：
//
//	agent2api                 # 启动网关
//	agent2api login           # 设备码登录（新机器 / 多账号场景）
//	agent2api models          # 打印当前账号可用模型
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/app"
	"github.com/576469377/Agent2API/internal/config"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin(os.Args[2:])
			return
		case "models":
			runModels(os.Args[2:])
			return
		case "dedupe":
			runDedupe(os.Args[2:])
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}
	runServer(os.Args[1:])
}

func printUsage() {
	fmt.Fprint(os.Stderr, `agent2api — 多平台 Agent 反向代理网关

用法:
  agent2api [flags]            启动网关服务
  agent2api login [flags]      设备码登录，获取上游凭证
  agent2api models [flags]     列出当前账号可用模型
  agent2api dedupe [flags]     清理凭证目录里的重复与无效凭证

启动参数:
`)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addServerFlags(fs)
	fs.SetOutput(os.Stderr)
	_ = fs.Parse([]string{})
	fs.PrintDefaults()
}

// ───────────────────────── 启动参数 ─────────────────────────

type serverFlags struct {
	configPath     string
	host           string
	port           int
	apiKey         string
	credentialPath string
	accountsDir    string
	baseURL        string
	noSanitize     bool
	platform       string
	metricsFile    string
	noPersist      bool
}

func addServerFlags(fs *flag.FlagSet) *serverFlags {
	sf := &serverFlags{}
	fs.StringVar(&sf.configPath, "config", "", "配置文件路径（JSON）")
	fs.StringVar(&sf.host, "host", "", "监听地址，默认 127.0.0.1")
	fs.IntVar(&sf.port, "port", 0, "监听端口，默认 8787")
	fs.StringVar(&sf.apiKey, "api-key", "", "网关访问密钥；为空则不鉴权")
	fs.StringVar(&sf.credentialPath, "credential", "", "上游凭证文件路径；为空时自动探测本机已登录凭证")
	fs.StringVar(&sf.accountsDir, "accounts-dir", "", "多账号号池目录；目录下每个 *.json 视为一个账号（agent2api login -out 攒凭证）")
	fs.StringVar(&sf.baseURL, "base-url", "", "上游地址，默认 https://copilot.tencent.com")
	fs.BoolVar(&sf.noSanitize, "no-sanitize", false, "关闭内容脱敏（接入 Claude Code/Codex 时不建议关闭）")
	fs.StringVar(&sf.platform, "platform", "", "上游平台（当前仅支持 workbuddy；缺省自动探测）")
	fs.StringVar(&sf.metricsFile, "metrics-file", "", "指标落盘路径；默认写到配置文件同目录的 metrics.json")
	fs.BoolVar(&sf.noPersist, "no-persist", false, "关闭指标落盘（重启后统计清零）")
	return sf
}

func (sf *serverFlags) apply(cfg *config.Config) {
	if sf.host != "" {
		cfg.Server.Host = sf.host
	}
	if sf.port != 0 {
		cfg.Server.Port = sf.port
	}
	if sf.apiKey != "" {
		cfg.Auth.APIKey = sf.apiKey
	}
	if sf.credentialPath != "" {
		cfg.Upstream.CredentialPath = sf.credentialPath
	}
	if sf.accountsDir != "" {
		cfg.Upstream.AccountsDir = sf.accountsDir
	}
	if sf.baseURL != "" {
		cfg.Upstream.BaseURL = sf.baseURL
	}
	if sf.noSanitize {
		cfg.Upstream.Sanitize = false
	}
	if sf.platform != "" {
		cfg.Upstream.Platform = sf.platform
	}
}

// resolveMetricsFile 决定指标落盘路径：
//
//	-no-persist            → 关闭持久化（空路径）
//	-metrics-file <path>   → 直接使用
//	有配置文件             → <配置目录>/metrics.json
//	都没有                → <工作目录>/agent2api-metrics.json
func resolveMetricsFile(sf *serverFlags, cfg *config.Config) {
	if sf.noPersist {
		cfg.MetricsFile = ""
		return
	}
	if sf.metricsFile != "" {
		cfg.MetricsFile = sf.metricsFile
		return
	}
	if cfg.MetricsFile != "" {
		return
	}
	if sf.configPath != "" {
		cfg.MetricsFile = filepath.Join(filepath.Dir(sf.configPath), "metrics.json")
		return
	}
	if wd, err := os.Getwd(); err == nil {
		cfg.MetricsFile = filepath.Join(wd, "agent2api-metrics.json")
	}
}

// ───────────────────────── 服务 ─────────────────────────

func runServer(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sf := addServerFlags(fs)
	_ = fs.Parse(args)

	cfg, err := config.Load(sf.configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	sf.apply(&cfg)
	resolveMetricsFile(sf, &cfg)
	// 号池目录默认值：配置/参数都没给时，若工作目录存在 auths/ 则自动启用——
	// 用户把凭证文件拖进去就生效，无需改配置。
	if cfg.Upstream.AccountsDir == "" {
		if st, err := os.Stat("auths"); err == nil && st.IsDir() {
			cfg.Upstream.AccountsDir = "auths"
		}
	}

	// 平台合法性在 buildHub 里逐个校验（自动集成模式下 Platform 为空是正常的）。
	// 这里只拦「显式写了不支持平台」的低级错误，快速失败。
	if cfg.Upstream.Platform != "" && cfg.Upstream.Platform != "workbuddy" {
		log.Fatalf("暂不支持的平台: %s（当前支持 workbuddy）", cfg.Upstream.Platform)
	}

	logger := log.New(os.Stdout, "[agent2api] ", log.LstdFlags|log.Lmicroseconds)

	hub, err := buildHub(cfg, logger)
	if err != nil {
		logger.Fatalf("初始化上游适配器失败: %v", err)
	}
	// 号池热加载：控制台「添加账号」或手动放入号池目录的新凭证，
	// 运行期自动入池，无需重启。
	startPoolWatchers(hub, cfg, logger)
	startKeepalive(hub, logger)
	// 注意：这里刻意**不**打印「上游平台=… 账号=…」——那些信息在下面的横幅里
	// 有更可读的呈现，重复一行带时间戳前缀的日志只会干扰阅读。

	application := app.New(cfg, hub, logger)
	application.StartMetricsPersistence(30 * time.Second)
	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           application.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutSec) * time.Second,
		// 不设 WriteTimeout：流式响应可能持续数十分钟。
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("服务异常退出: %v", err)
		}
	}()

	// 启动横幅直接写 stdout（不经过 logger，避免时间戳前缀干扰阅读与复制）。
	printBanner(cfg, hub)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// 退出前把指标落盘，下次启动可恢复历史统计。
	application.PersistMetricsNow()
	logger.Printf("正在关闭...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Printf("关闭超时: %v", err)
	}
	logger.Printf("已停止")
}

// baseURL 生成对外展示的访问地址。
//
// 监听 0.0.0.0 / :: 时展示 localhost：那才是用户实际能打开的地址，
// 直接打印 0.0.0.0 会让人以为地址写错了。
func baseURL(cfg config.Config) string {
	host := cfg.Server.Host
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
}

// ───────────────────────── 终端显示辅助 ─────────────────────────

// dispWidth 估算字符串的终端显示宽度：CJK 等全角字符按 2 列计。
// 横幅要列对齐中英文混排的内容，按 rune 数计算会错位。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		w++
		if r > 0x2E80 { // CJK 部首区起点，覆盖汉字/假名/谚文
			w++
		}
	}
	return w
}

// padRight 右侧补空格到指定显示宽度。
func padRight(s string, w int) string {
	if d := w - dispWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// printBanner 打印启动横幅。
//
// 版式原则：**所有内容都从运行时状态生成**——上游、账号（友好名）、模型数
// 全部来自 hub 探测，不写死任何平台或账号；接口只列路径（完整地址在头部
// 出现一次，避免每行重复一长串）；提示区只放需要用户行动或知晓的事。
func printBanner(cfg config.Config, hub *app.Hub) {
	base := baseURL(cfg)
	line := strings.Repeat("─", 62)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	type accountLine struct{ mark, label, detail string }
	type upRow struct {
		mark, id, account, models string
		details                   []accountLine
	}

	var rows []upRow
	totalModels, totalAccounts, healthyAccounts := 0, 0, 0
	for _, rt := range hub.Platforms() {
		sts := rt.AccountStatuses()
		healthy := 0
		names := make([]string, 0, len(sts))
		row := upRow{id: rt.ID}
		for _, st := range sts {
			totalAccounts++
			names = append(names, st.Label)
			switch {
			case st.State == "disabled":
				row.details = append(row.details, accountLine{"⏸", st.Label, "已停用"})
			case !st.Healthy:
				detail := truncate(st.LastError, 46)
				if detail == "" {
					detail = "不可用"
				}
				if st.CooldownSecs > 0 {
					detail = fmt.Sprintf("冷却 %ds · %s", st.CooldownSecs, detail)
				}
				row.details = append(row.details, accountLine{"⏳", st.Label, detail})
			default:
				healthy++
			}
		}
		healthyAccounts += healthy

		row.account = strings.Join(names, "、")
		if len(names) > 2 {
			row.account = fmt.Sprintf("%s 等 %d 个账号", names[0], len(names))
		}
		switch {
		case healthy == 0:
			row.mark = "✗"
		case healthy < len(sts):
			row.mark = "◐"
		default:
			row.mark = "✓"
		}
		if ms, err := rt.Adapter.ListModels(ctx); err == nil {
			row.models = fmt.Sprint(len(ms))
			totalModels += len(ms)
		} else {
			row.models = "—"
		}
		rows = append(rows, row)
	}

	fmt.Println()
	fmt.Println(line)
	fmt.Println("  Agent2API · 统一网关与控制台")
	fmt.Printf("  %s · %d 个上游 · %d 个账号（%d 可用）· %d 个模型\n",
		base, len(rows), totalAccounts, healthyAccounts, totalModels)

	// ── 上游区：列宽按内容自适应，中英文混排也对齐 ──
	if len(rows) > 0 {
		idW, accW := 0, 0
		for _, r := range rows {
			idW = maxInt(idW, dispWidth(r.id))
			accW = maxInt(accW, dispWidth(r.account))
		}
		fmt.Println()
		fmt.Println("  上游")
		for _, r := range rows {
			fmt.Printf("    %s %s  %s  %s 个模型\n", r.mark, padRight(r.id, idW), padRight(r.account, accW), r.models)
			for _, d := range r.details {
				fmt.Printf("        %s %s · %s\n", d.mark, d.label, d.detail)
			}
		}
	}

	// ── 接口区：完整地址只在头部出现一次，这里只列路径 ──
	type endpoint struct{ method, path, desc string }
	eps := []endpoint{
		{"POST", "/v1/chat/completions", "OpenAI Chat 兼容"},
		{"POST", "/v1/responses", "OpenAI Responses"},
		{"POST", "/v1/messages", "Anthropic Messages 兼容"},
		{"GET", "/v1/models", "模型清单"},
	}
	pw := 0
	for _, e := range eps {
		pw = maxInt(pw, len(e.method)+1+len(e.path))
	}
	fmt.Println()
	fmt.Println("  接口")
	for _, e := range eps {
		fmt.Printf("    %s  %s\n", padRight(e.method+" "+e.path, pw), e.desc)
	}
	fmt.Println(line)
}

// supportedPlatforms 是 buildHub 能装配的平台集合（与 newPlatformAdapter 工厂一致）。
// 新增平台适配器后在此追加。
var supportedPlatforms = map[string]bool{"workbuddy": true}

// buildHub 按配置装配多平台枢纽。
//
// ResolvePlatforms 得到要集成的平台列表后，每个平台独立构建适配器/号池：
// 单个平台失败（凭证缺失、文件损坏、不认识的平台）只跳过并告警，
// 其余平台照常服务；全部失败才返回错误。
// 多账号、凭证探测、号池冷却等逻辑全部复用 buildAdapter，只是按平台各跑一遍。
func buildHub(cfg config.Config, logger *log.Logger) (*app.Hub, error) {
	pcs := cfg.ResolvePlatforms()
	var runtimes []*app.PlatformRuntime
	var failures []string
	for _, pc := range pcs {
		if !supportedPlatforms[pc.ID] {
			failures = append(failures, pc.ID+": 不支持的平台")
			logger.Printf("平台 %s 不受支持，已跳过（当前支持 workbuddy）", pc.ID)
			continue
		}
		clone := cfg.CloneForPlatform(pc)
		adp, pool, err := buildAdapter(clone, logger)
		if err != nil {
			failures = append(failures, pc.ID+": "+err.Error())
			logger.Printf("平台 %s 初始化失败，已跳过: %v", pc.ID, err)
			continue
		}
		runtimes = append(runtimes, &app.PlatformRuntime{ID: pc.ID, Adapter: adp, Pool: pool, Cfg: pc})
	}
	if len(runtimes) == 0 {
		return nil, fmt.Errorf("所有平台初始化失败: %s", strings.Join(failures, "; "))
	}
	hub, err := app.NewHub(runtimes, logger)
	if err != nil {
		return nil, err
	}
	// 模型别名（models.aliases）：客户端固定模型名 → 账号实际可用的模型。
	// 必须在开始服务之前装载（运行期只读，避免查询到半张表）。
	hub.SetAliases(cfg.Models.Aliases)
	return hub, nil
}

// truncate 按字符截断长文本用于单行展示。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ───────────────────────── 登录 ─────────────────────────

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	out := fs.String("out", "", "凭证输出路径，默认 ~/.workbuddy/session.json")
	base := fs.String("base-url", workbuddy.DefaultBaseURL, "上游地址")
	platform := fs.String("platform", "VSCode", "上报给上游的客户端标识")
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	cred, err := workbuddy.Login(ctx, *base, workbuddy.LoginOptions{
		BaseURL:    *base,
		Platform:   *platform,
		OutputPath: *out,
		OnAuthURL: func(url string) {
			fmt.Println("\n请在浏览器中打开下面的地址完成登录：")
			fmt.Println("  " + url)
			fmt.Println("\n等待授权中（最长 5 分钟）...")
		},
	})
	if err != nil {
		log.Fatalf("登录失败: %v", err)
	}
	if cred.Nickname != "" {
		fmt.Printf("登录成功: %s\n", cred.Nickname)
	} else {
		fmt.Println("登录成功")
	}
	fmt.Println("现在可以启动网关: agent2api")
	fmt.Println("多账号提示: agent2api login -out auths/a.json 可把凭证存入号池目录，多号轮询使用")
}

// ───────────────────────── 模型列表 ─────────────────────────

func runModels(args []string) {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径")
	credentialPath := fs.String("credential", "", "凭证文件路径")
	baseURL := fs.String("base-url", "", "上游地址")
	platform := fs.String("platform", "", "只列指定平台（缺省列出全部已集成平台）")
	_ = fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if *credentialPath != "" {
		cfg.Upstream.CredentialPath = *credentialPath
	}
	if *baseURL != "" {
		cfg.Upstream.BaseURL = *baseURL
	}

	logger := log.New(os.Stderr, "", 0)
	hub, err := buildHub(cfg, logger)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, rt := range hub.Platforms() {
		if *platform != "" && rt.ID != *platform {
			continue
		}
		models, err := rt.Adapter.ListModels(ctx)
		if err != nil {
			fmt.Printf("平台 %s 获取模型失败: %v\n", rt.ID, err)
			continue
		}
		fmt.Printf("平台 %s · 共 %d 个可用模型：\n", rt.ID, len(models))
		for _, m := range models {
			flags := ""
			if m.SupportsTools {
				flags += " tools"
			}
			if m.SupportsThinking {
				flags += " thinking"
			}
			if m.SupportsImages {
				flags += " vision"
			}
			fmt.Printf("  %-24s %-22s ctx=%d%s\n", m.ID, m.DisplayName, m.ContextTokens, flags)
		}
	}
}

// runDedupe 清理凭证目录里的重复与无效凭证。
//
//	agent2api dedupe            预览（不删）
//	agent2api dedupe -yes       实际清理
func runDedupe(args []string) {
	fs := flag.NewFlagSet("dedupe", flag.ExitOnError)
	dir := fs.String("dir", "", "凭证目录，默认 ~/.workbuddy")
	yes := fs.Bool("yes", false, "实际执行删除（默认只预览）")
	_ = fs.Parse(args)

	target := *dir
	if target == "" {
		target = app.DefaultAccountsDir()
	}
	fmt.Printf("凭证目录: %s\n\n", target)

	removed, kept := dedupeCredentialDir(target, !*yes, func(format string, a ...any) {
		fmt.Printf("  %s\n", fmt.Sprintf(format, a...))
	})
	fmt.Printf("\n保留 %d 份，%s %d 份\n", kept, map[bool]string{true: "待删除", false: "已删除"}[!*yes], removed)
	if !*yes && removed > 0 {
		fmt.Println("\n这是预览。确认无误后加 -yes 实际执行。")
	}
}
