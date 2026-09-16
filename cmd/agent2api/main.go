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

	"github.com/576469377/Agent2API/internal/adapter"
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
	fs.StringVar(&sf.platform, "platform", "workbuddy", "上游平台，目前仅支持 workbuddy")
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

	if cfg.Upstream.Platform != "workbuddy" {
		log.Fatalf("暂不支持的平台: %s（当前仅实现 workbuddy）", cfg.Upstream.Platform)
	}

	logger := log.New(os.Stdout, "[agent2api] ", log.LstdFlags|log.Lmicroseconds)

	adp, pool, err := buildAdapter(cfg, logger)
	if err != nil {
		logger.Fatalf("初始化上游适配器失败: %v", err)
	}
	logger.Printf("上游平台=%s 账号=%s", adp.Name(), summarizeAccounts(adp, pool))

	application := app.New(cfg, adp, logger)
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
	printBanner(cfg, adp, pool)

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

// platformSummary 汇总平台与账号信息，含一次轻量的模型探测。
//
// 探测失败不阻断启动，只是不显示模型数量——启动不该强依赖上游可用性。
func platformSummary(adp adapter.Adapter, pool *adapter.Pool) string {
	base := fmt.Sprintf("%s · 账号 %s", adp.Name(), summarizeAccounts(adp, pool))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := adp.ListModels(ctx)
	if err != nil {
		return base + " · 模型获取失败（上游不可达）"
	}
	return fmt.Sprintf("%s · %d 个模型", base, len(models))
}

// printBanner 打印启动横幅，把控制台地址放在最显眼的位置。
func printBanner(cfg config.Config, adp adapter.Adapter, pool *adapter.Pool) {
	base := baseURL(cfg)
	line := strings.Repeat("─", 62)

	fmt.Println()
	fmt.Println(line)
	fmt.Printf("  Agent2API 控制台    %s/\n", base)
	fmt.Println()
	fmt.Printf("  %s\n", platformSummary(adp, pool))
	// 多账号时逐个列出账号状态，让「谁在冷却」一眼可见。
	if pool != nil && pool.Len() > 1 {
		for _, st := range pool.Statuses() {
			flag := "✓"
			detail := ""
			if !st.Healthy {
				flag = "⏳"
				detail = fmt.Sprintf(" · 冷却 %ds · %s", st.CooldownSecs, truncate(st.LastError, 40))
			}
			fmt.Printf("        %s %s%s\n", flag, st.Label, detail)
		}
	}
	fmt.Println(line)
	fmt.Println("  接口")
	fmt.Printf("    POST  %s/v1/chat/completions   OpenAI Chat Completions\n", base)
	fmt.Printf("    POST  %s/v1/responses          OpenAI Responses\n", base)
	fmt.Printf("    POST  %s/v1/messages           Anthropic Messages\n", base)
	fmt.Printf("    GET   %s/v1/models             模型清单\n", base)
	fmt.Printf("    GET   %s/health                健康检查\n", base)
	fmt.Println(line)

	if cfg.Auth.APIKey == "" {
		fmt.Println("  提示  未配置 API Key，网关对本机可访问者开放（启动时加 -api-key 设置）")
	} else {
		fmt.Println("  提示  访问鉴权已启用")
	}
	if cfg.MetricsFile != "" {
		fmt.Printf("        指标持久化已开启（%s），重启不丢失统计\n", cfg.MetricsFile)
	} else {
		fmt.Println("        指标持久化已关闭（重启后统计清零，加 -metrics-file 启用）")
	}
	fmt.Println("        Ctrl+C 停止服务")
	fmt.Println()
}

// newAdapter 构造单账号适配器（models 子命令等简单场景使用）。
func newAdapter(cfg config.Config, logger *log.Logger) (*workbuddy.Adapter, error) {
	return workbuddy.New(workbuddy.Config{
		BaseURL:            cfg.Upstream.BaseURL,
		CredentialPath:     cfg.Upstream.CredentialPath,
		Sanitize:           cfg.Upstream.Sanitize,
		RequestTimeout:     cfg.RequestTimeout(),
		StreamIdleTimeout:  cfg.StreamIdleTimeout(),
		StreamTotalTimeout: cfg.StreamTotalTimeout(),
		ModelCacheTTL:      cfg.ModelCacheTTL(),
		Logger: func(format string, args ...any) {
			logger.Printf(format, args...)
		},
	})
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
	adp, err := newAdapter(cfg, logger)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	models, err := adp.ListModels(ctx)
	if err != nil {
		log.Fatalf("获取模型失败: %v", err)
	}
	fmt.Printf("共 %d 个可用模型：\n", len(models))
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
