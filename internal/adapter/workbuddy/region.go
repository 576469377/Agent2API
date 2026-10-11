package workbuddy

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const GlobalPlatform = "workbuddy-global"
const GlobalBaseURL = "https://www.workbuddy.ai"

func BaseURLForPlatform(platform string) string {
	if platform == GlobalPlatform {
		return GlobalBaseURL
	}
	return DefaultBaseURL
}

func AccountsDirForPlatform(platform string) string {
	home, _ := os.UserHomeDir()
	if platform == GlobalPlatform {
		return filepath.Join(home, ".workbuddy-ai")
	}
	return filepath.Join(home, ".workbuddy")
}

// DefaultCredentialPathsForPlatform keeps domestic and international sessions separate.
func DefaultCredentialPathsForPlatform(platform string) []string {
	paths := DefaultCredentialPaths()
	if platform != GlobalPlatform {
		return paths
	}
	for i, p := range paths {
		p = strings.ReplaceAll(p, "workbuddy-desktop.info", "workbuddy-desktop-ai.info")
		p = strings.ReplaceAll(p, ".workbuddy/", ".workbuddy-ai/")
		p = strings.ReplaceAll(p, `.workbuddy\`, `.workbuddy-ai\`)
		p = strings.ReplaceAll(p, ".codebuddy-session.json", ".workbuddy-ai/session.json")
		paths[i] = filepath.FromSlash(p)
	}
	return paths
}

func globalDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if strings.Contains(domain, "://") {
		if u, err := url.Parse(domain); err == nil {
			domain = u.Hostname()
		}
	}
	return domain == "workbuddy.ai" || strings.HasSuffix(domain, ".workbuddy.ai") ||
		domain == "codebuddy.ai" || strings.HasSuffix(domain, ".codebuddy.ai") || domain == "staging-codebuddy.tencent.com"
}
