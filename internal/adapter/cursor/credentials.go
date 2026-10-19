// Package cursor connects a signed-in personal Free Cursor account to the gateway.
package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type credentials struct{ token, version, commit, machine, mac string }

func dataDir() string {
	if p := os.Getenv("CURSOR_USER_DATA_DIR"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Cursor")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor")
	default:
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			root = filepath.Join(home, ".config")
		}
		return filepath.Join(root, "Cursor")
	}
}

func readCredentials(ctx context.Context, db string) (credentials, error) {
	var c credentials
	if db == "" {
		db = filepath.Join(dataDir(), "User", "globalStorage", "state.vscdb")
	}
	c.token = strings.TrimSpace(os.Getenv("CURSOR_ACCESS_TOKEN"))
	if c.token == "" {
		var err error
		c.token, err = readDesktopToken(ctx, db)
		if err != nil || c.token == "" {
			return c, fmt.Errorf("Cursor 登录态不可读，请在当前用户的 Cursor 中登录")
		}
	}
	var state map[string]json.RawMessage
	if raw, err := os.ReadFile(filepath.Join(dataDir(), "User", "globalStorage", "storage.json")); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	_ = json.Unmarshal(state["telemetry.machineId"], &c.machine)
	_ = json.Unmarshal(state["telemetry.macMachineId"], &c.mac)
	roots := []string{os.Getenv("CURSOR_CLIENT_PATH")}
	switch runtime.GOOS {
	case "windows":
		roots = append(roots, filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "cursor", "resources", "app"))
	case "darwin":
		roots = append(roots, "/Applications/Cursor.app/Contents/Resources/app")
	default:
		roots = append(roots, "/opt/Cursor/resources/app", "/usr/share/cursor/resources/app")
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		var pkg struct {
			Version string `json:"version"`
		}
		var product struct {
			Commit string `json:"commit"`
		}
		p, e1 := os.ReadFile(filepath.Join(root, "package.json"))
		q, e2 := os.ReadFile(filepath.Join(root, "product.json"))
		if e1 == nil && e2 == nil && json.Unmarshal(p, &pkg) == nil && json.Unmarshal(q, &product) == nil && pkg.Version != "" {
			c.version, c.commit = pkg.Version, product.Commit
			break
		}
	}
	if c.version == "" || c.machine == "" {
		return credentials{}, fmt.Errorf("Cursor 当前安装版本或原有设备标识不可读，请检查 CURSOR_CLIENT_PATH / CURSOR_USER_DATA_DIR")
	}
	return c, nil
}

func (c credentials) checksum(now time.Time) string {
	// This is the installed client's timestamp encoding, including JavaScript's
	// 32-bit shift behavior. Reuse existing IDs; never create/reset device IDs.
	stamp := uint32(now.UnixMilli() / 1_000_000)
	b := make([]byte, 6)
	prior := byte(165)
	for i, shift := range []uint{40, 32, 24, 16, 8, 0} {
		b[i] = (byte(stamp>>(shift%32)) ^ prior) + byte(i)
		prior = b[i]
	}
	value := base64.StdEncoding.EncodeToString(b) + c.machine
	if c.mac != "" {
		value += "/" + c.mac
	}
	return value
}

func versionAtLeast(current, minimum string) bool {
	parse := func(v string) ([3]int, bool) {
		var out [3]int
		parts := strings.SplitN(strings.SplitN(v, "-", 2)[0], ".", 4)
		if len(parts) != 3 {
			return out, false
		}
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	a, ok := parse(current)
	b, valid := parse(minimum)
	if !ok || !valid {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}
