package kiro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

type LoginOptions struct {
	CLIPath, Provider string
	OnChallenge       func(code, authorizationURL string)
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
var deviceCode = regexp.MustCompile(`\b[A-Z]{4}-[A-Z]{4}\b`)
var loginURL = regexp.MustCompile(`https://[^\s<>"\x1b]+`)

func safeAuthorizationURL(raw string) string {
	u, err := url.Parse(strings.TrimRight(raw, ".,)"))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "kiro.dev" || strings.HasSuffix(h, ".kiro.dev") || h == "awsapps.com" || strings.HasSuffix(h, ".awsapps.com") || h == "signin.aws.amazon.com" {
		return u.String()
	}
	return ""
}

// Login delegates credentials and browser/device authorization entirely to the
// official CLI. It never reads, exports, or rewrites the native credential store.
func Login(ctx context.Context, opts LoginOptions) error {
	exe, err := ResolveCLI(opts.CLIPath)
	if err != nil {
		return err
	}
	args := []string{"login", "--use-device-flow"}
	switch opts.Provider {
	case "google", "github":
		args = append(args, "--social", opts.Provider)
	case "builderid":
		args = append(args, "--license", "free")
	default:
		return clientFailure("kiro_login_provider", "请选择 Google、GitHub 或 AWS Builder ID")
	}
	cmd := cliCommand(ctx, exe, "", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if cmd.Start() != nil {
		return upstreamFailure("kiro_cli_start", "Kiro CLI 登录无法启动")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	code, authURL := "", ""
	read := func(src io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(src)
		s.Buffer(make([]byte, 4096), 256*1024)
		for s.Scan() {
			line := ansiEscape.ReplaceAllString(s.Text(), "")
			mu.Lock()
			changed := false
			if v := deviceCode.FindString(line); v != "" && v != code {
				code = v
				changed = true
			}
			for _, v := range loginURL.FindAllString(line, -1) {
				if v = safeAuthorizationURL(v); v != "" && v != authURL {
					authURL = v
					changed = true
				}
			}
			if changed && opts.OnChallenge != nil {
				opts.OnChallenge(code, authURL)
			}
			mu.Unlock()
		}
	}
	wg.Add(2)
	go read(out)
	go read(stderr)
	wg.Wait()
	if cmd.Wait() != nil {
		if ctx.Err() != nil {
			return clientFailure("kiro_login_expired", "Kiro 授权已超时，请重新开始网页登录")
		}
		return clientFailure("kiro_login_failed", "Kiro 官方 CLI 未完成授权，请重新打开网页登录")
	}
	check := cliCommand(ctx, exe, "", "whoami", "--format", "json")
	var result bytes.Buffer
	check.Stdout = &result
	check.Stderr = io.Discard
	if check.Run() != nil {
		return clientFailure("kiro_login_unconfirmed", "Kiro 授权完成但登录态尚未确认，请检测连接")
	}
	var identity struct {
		Type string `json:"accountType"`
	}
	if json.Unmarshal(result.Bytes(), &identity) != nil || identity.Type == "" {
		return clientFailure("kiro_login_unconfirmed", "Kiro CLI 尚未确认账号登录态")
	}
	return nil
}
