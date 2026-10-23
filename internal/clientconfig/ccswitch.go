package clientconfig

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"
)

const ccPrefix = "agent2api-free-"

func CCSwitchPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cc-switch", "cc-switch.db")
}

func localBase(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "http" && u.User == nil && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
}

func openCC(path string, readOnly bool) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("未检测到 cc-switch 数据库，请先启动 cc-switch 一次")
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("无法安全读取 cc-switch 数据库")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("cc-switch 数据库路径无效")
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(3000)", "foreign_keys(1)"}, "_txlock": {"immediate"}}
	if readOnly {
		q.Set("mode", "ro")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cc-switch 数据库无法打开")
	}
	db.SetMaxOpenConns(1)
	rows, err := db.Query("PRAGMA table_info(providers)")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("cc-switch 数据库不可读或正被占用")
	}
	columns := map[string]bool{}
	valid := true
	known := map[string]bool{}
	for _, key := range strings.Fields("id app_type name settings_config meta is_current in_failover_queue created_at sort_index category notes website_url icon icon_color") {
		known[key] = true
	}
	for rows.Next() {
		var index, required, pk int
		var name, kind string
		var defaultValue any
		if rows.Scan(&index, &name, &kind, &required, &defaultValue, &pk) != nil {
			valid = false
			break
		}
		columns[name] = true
		if required != 0 && defaultValue == nil && !known[name] {
			valid = false
		}
	}
	valid = valid && rows.Err() == nil
	rows.Close()
	for _, key := range strings.Fields("id app_type name settings_config meta is_current in_failover_queue created_at sort_index category notes") {
		valid = valid && columns[key]
	}
	if !valid {
		db.Close()
		return nil, fmt.Errorf("当前 cc-switch 数据库结构不支持安全同步，未修改数据库")
	}
	return db, nil
}

type CCRow struct {
	ID, App, Config, Meta string
	Current, Failover     bool
}

type CCSnapshot map[string]CCRow

type ccQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readCC(ctx context.Context, db ccQuery) (CCSnapshot, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,app_type,settings_config,meta,is_current,in_failover_queue FROM providers WHERE substr(id,1,?)=? ORDER BY app_type,id", len(ccPrefix), ccPrefix)
	if err != nil {
		return nil, fmt.Errorf("无法读取 cc-switch 供应商配置")
	}
	defer rows.Close()
	out := CCSnapshot{}
	for rows.Next() {
		var row CCRow
		if err := rows.Scan(&row.ID, &row.App, &row.Config, &row.Meta, &row.Current, &row.Failover); err != nil {
			return nil, fmt.Errorf("cc-switch 供应商条目格式无效")
		}
		out[row.App+"/"+row.ID] = row
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cc-switch 供应商读取未完成")
	}
	return out, nil
}

func ReadCC(ctx context.Context, path string) (CCSnapshot, error) {
	db, err := openCC(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return readCC(ctx, db)
}

func ccID(m Model) string {
	hash := sha256.Sum256([]byte(Route(m)))
	return ccPrefix + m.Platform + "-" + hex.EncodeToString(hash[:8])
}

func ccMarker(m Model) object {
	return object{"schema": 1, "platform": m.Platform, "route": Route(m)}
}

func ccOwned(row CCRow) (object, string, error) {
	meta, err := parse([]byte(row.Meta))
	if err != nil {
		return nil, "", fmt.Errorf("cc-switch 同名供应商的元数据无效，未接管条目")
	}
	marker, _ := meta["agent2api"].(object)
	platform, _ := marker["platform"].(string)
	route, _ := marker["route"].(string)
	if marker["schema"] != json.Number("1") || platform == "" || !strings.HasPrefix(route, platform+"/") {
		platform, route = "", ""
		// cc-switch 的编辑器可能丢弃未知 meta 字段，仍需核对连接、模型及完整确定性 ID。
		cfg, e := parse([]byte(row.Config))
		if e == nil && row.App == "claude" {
			env, _ := cfg["env"].(object)
			if localBase(env["ANTHROPIC_BASE_URL"]) {
				route, _ = env["ANTHROPIC_MODEL"].(string)
			}
		} else if e == nil && row.App == "codex" {
			text, _ := cfg["config"].(string)
			var config object
			if toml.Unmarshal([]byte(text), &config) == nil && config["model_provider"] == "agent2api" {
				providers, _ := config["model_providers"].(object)
				provider, _ := providers["agent2api"].(object)
				if localBase(provider["base_url"]) {
					route, _ = config["model"].(string)
				}
			}
		}
		if before, _, found := strings.Cut(route, "/"); found {
			platform = before
		}
		if platform == "" || route == "" {
			return nil, "", fmt.Errorf("cc-switch 存在同名且未经本工具管理的供应商，未覆盖条目")
		}
		meta["agent2api"] = object{"schema": 1, "platform": platform, "route": route}
	}
	hash := sha256.Sum256([]byte(route))
	if row.ID != ccPrefix+platform+"-"+hex.EncodeToString(hash[:8]) {
		return nil, "", fmt.Errorf("cc-switch 供应商标识与模型不一致，原配置已保留")
	}
	return meta, platform, nil
}

func ccConfig(row CCRow, model Model, baseURL, key string) (string, error) {
	doc := object{}
	var err error
	if row.Config != "" {
		doc, err = parse([]byte(row.Config))
		if err != nil {
			return "", err
		}
	}
	route := Route(model)
	if row.App == "claude" {
		env, err := child(doc, "env")
		if err != nil {
			return "", err
		}
		env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_AUTH_TOKEN"] = strings.TrimRight(baseURL, "/"), key
		env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = strconv.Itoa(OutputLimit(model))
		delete(env, "ANTHROPIC_API_KEY")
		for _, field := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_REASONING_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
			env[field] = route
		}
		doc["model"] = route
	} else {
		auth, err := child(doc, "auth")
		if err != nil {
			return "", err
		}
		auth["OPENAI_API_KEY"] = key
		config := object{}
		if value, exists := doc["config"]; exists {
			text, ok := value.(string)
			if !ok || toml.Unmarshal([]byte(text), &config) != nil {
				return "", fmt.Errorf("cc-switch 中的 Codex TOML 配置无效，未覆盖条目")
			}
		}
		config["model_provider"], config["model"] = "agent2api", route
		config["model_context_window"] = model.ContextTokens
		providers, err := child(config, "model_providers")
		if err != nil {
			return "", err
		}
		provider, err := child(providers, "agent2api")
		if err != nil {
			return "", err
		}
		provider["name"], provider["base_url"], provider["wire_api"], provider["requires_openai_auth"] = "Agent2API", strings.TrimRight(baseURL, "/")+"/v1", "responses", true
		provider["supports_websockets"] = false
		delete(provider, "env_key")
		delete(provider, "http_headers")
		delete(provider, "env_http_headers")
		encoded, err := toml.Marshal(config)
		if err != nil {
			return "", fmt.Errorf("无法生成 Codex 配置")
		}
		doc["config"] = string(encoded)
	}
	raw, err := json.Marshal(doc)
	return string(raw), err
}

type ccChange struct {
	row         CCRow
	name        string
	add, remove bool
}

// SyncCC 用事务更新管理范围内的条目，不切换当前供应商，也不写 Claude/Codex 的实时配置。
func SyncCC(ctx context.Context, path string, source CCSnapshot, models []Model, platforms, apps []string, baseURL, key string, now time.Time) (Result, error) {
	chosen, err := selected(models, platforms, now)
	if err != nil {
		return Result{}, err
	}
	appSet := map[string]bool{}
	for _, app := range apps {
		if app != "claude" && app != "codex" {
			return Result{}, fmt.Errorf("cc-switch 仅支持 Claude Code 与 Codex 配置")
		}
		appSet[app] = true
	}
	if len(appSet) == 0 {
		return Result{}, fmt.Errorf("请至少选择一个 cc-switch 应用")
	}
	if key == "" {
		key = "agent2api-local"
	}
	result := newResult(chosen)
	wanted, platformSet := map[string]bool{}, map[string]bool{}
	for _, p := range platforms {
		platformSet[p] = true
	}
	changes := []ccChange{}
	for app := range appSet {
		result.Apps = append(result.Apps, app)
		for _, m := range chosen {
			id := ccID(m)
			row, exists := source[app+"/"+id]
			if !exists {
				row = CCRow{ID: id, App: app, Meta: "{}"}
			}
			meta := object{}
			if exists {
				meta, _, err = ccOwned(row)
				if err != nil {
					return Result{}, err
				}
			}
			meta["agent2api"] = ccMarker(m)
			meta["commonConfigEnabled"], meta["endpointAutoSelect"] = false, false
			if app == "claude" {
				meta["apiFormat"] = "anthropic"
			}
			previous := row
			row.Config, err = ccConfig(row, m, baseURL, key)
			if err != nil {
				return Result{}, err
			}
			encoded, _ := json.Marshal(meta)
			row.Meta = string(encoded)
			wanted[app+"/"+id] = true
			result.ProviderCount++
			if exists {
				result.Retained++
			} else {
				result.Added = append(result.Added, app+"/"+Route(m))
			}
			if !exists || !sameJSON(previous.Config, row.Config) || !sameJSON(previous.Meta, row.Meta) {
				if previous.Current {
					return Result{}, fmt.Errorf("待更新的 Agent2API 供应商正在 cc-switch 中使用，请先切换到其他供应商后同步")
				}
				changes = append(changes, ccChange{row: row, name: "Agent2API · " + PlatformName(m.Platform) + " · " + m.ID, add: !exists})
			}
		}
	}
	for id, row := range source {
		if !appSet[row.App] || wanted[id] {
			continue
		}
		if !ccInScope(row, platformSet, appSet) {
			continue
		}
		meta, platform, err := ccOwned(row)
		if err != nil {
			return Result{}, err
		}
		if !platformSet[platform] {
			continue
		}
		if row.Current || row.Failover {
			return Result{}, fmt.Errorf("待移除的模型仍为 cc-switch 当前供应商或故障转移项，请先取消使用后同步")
		}
		marker := meta["agent2api"].(object)
		result.Removed = append(result.Removed, row.App+"/"+marker["route"].(string))
		changes = append(changes, ccChange{row: row, remove: true})
	}
	sort.Strings(result.Apps)
	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	db, err := openCC(path, false)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	// SQLite 在线备份包含已提交 WAL，不能直接复制正在使用的 .db 文件。
	if len(changes) > 0 {
		backup, err := os.CreateTemp(filepath.Dir(path), "cc-switch.db.bak-"+time.Now().Format("20060102-150405")+"-*")
		if err != nil {
			return Result{}, fmt.Errorf("无法创建 cc-switch 数据库备份")
		}
		backupPath := backup.Name()
		backup.Close()
		if err = os.Chmod(backupPath, 0o600); err == nil {
			_, err = db.ExecContext(ctx, "VACUUM INTO ?", backupPath)
		}
		if err != nil {
			os.Remove(backupPath)
			return Result{}, fmt.Errorf("cc-switch 在线备份失败，数据库未被修改")
		}
		result.BackupPath = backupPath
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("cc-switch 数据库正在写入，请稍后重试")
	}
	defer tx.Rollback()
	current, err := readCC(ctx, tx)
	if err != nil || !reflect.DeepEqual(ccScope(source, platformSet, appSet), ccScope(current, platformSet, appSet)) {
		return Result{}, fmt.Errorf("同步期间 cc-switch 的 Agent2API 条目已被修改，请重新同步；未覆盖条目")
	}
	for _, change := range changes {
		row := change.row
		switch {
		case change.remove:
			_, err = tx.ExecContext(ctx, "DELETE FROM provider_endpoints WHERE provider_id=? AND app_type=?", row.ID, row.App)
			if err == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM providers WHERE id=? AND app_type=?", row.ID, row.App)
			}
		case change.add:
			_, err = tx.ExecContext(ctx, "INSERT INTO providers(id,app_type,name,settings_config,meta,is_current,in_failover_queue,created_at,sort_index,category,notes) VALUES(?,?,?,?,?,0,0,?,(SELECT COALESCE(MAX(sort_index),0)+1 FROM providers WHERE app_type=?),'custom',?)", row.ID, row.App, change.name, row.Config, row.Meta, now.UnixMilli(), row.App, "Agent2API 免费模型；由本机控制台管理")
		default:
			_, err = tx.ExecContext(ctx, "UPDATE providers SET settings_config=?,meta=? WHERE id=? AND app_type=?", row.Config, row.Meta, row.ID, row.App)
		}
		if err != nil {
			return Result{}, fmt.Errorf("cc-switch 写入失败，事务已回滚；原配置已保留")
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("cc-switch 事务提交失败，请检测数据库状态后重试")
	}
	if len(changes) > 0 {
		result.Status = "updated"
	}
	return result, nil
}

func sameJSON(a, b string) bool {
	x, e1 := parse([]byte(a))
	y, e2 := parse([]byte(b))
	return e1 == nil && e2 == nil && reflect.DeepEqual(x, y)
}

func ccInScope(row CCRow, platforms, apps map[string]bool) bool {
	if !apps[row.App] {
		return false
	}
	value := strings.TrimPrefix(row.ID, ccPrefix)
	if value == row.ID {
		return false
	}
	index := strings.LastIndex(value, "-")
	return index > 0 && platforms[value[:index]]
}

func ccScope(snapshot CCSnapshot, platforms, apps map[string]bool) CCSnapshot {
	out := CCSnapshot{}
	for id, row := range snapshot {
		if ccInScope(row, platforms, apps) {
			out[id] = row
		}
	}
	return out
}
