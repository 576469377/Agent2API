package clientconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/pelletier/go-toml/v2"
)

func free(platform, id string, now time.Time) Model {
	zero := 0.0
	source := map[string]string{"workbuddy": "upstream_config", "workbuddy-global": "upstream_config", "openrouter": "openrouter_models", "cursor": "cursor_free_plan"}[platform]
	return Model{Platform: platform, ModelInfo: adapter.ModelInfo{ID: id, DisplayName: id, ContextTokens: 128000, MaxOutputTokens: 32000, SupportsTools: true, SupportsThinking: true, Pricing: &adapter.ModelPricing{Status: "free", Source: source, CreditMultiplier: &zero, ObservedAt: now.Format(time.RFC3339)}}}
}

func TestPiMergePreservesOtherProvidersAndNoOpBytes(t *testing.T) {
	now := time.Now().UTC()
	source := []byte("\xef\xbb\xbf" + `{"large":9007199254740993,"providers":{"other":{"apiKey":"keep","models":[{"id":"mine"}]},"agent2api-cursor":{"api":"openai-completions","baseUrl":"http://127.0.0.1:8787/v1","models":[{"id":"cursor/free","name":"My label","compat":{"requiresToolResultName":true}},{"id":"cursor/paid"}]}},"preference":true}`)
	models := []Model{free("cursor", "free", now), free("openrouter", "free", now), free("openrouter", "second", now)}
	candidate, result, err := PreparePi(source, models, []string{"cursor", "openrouter"}, "http://127.0.0.1:8787", "", now)
	if err != nil || result.ModelCount != 3 || len(result.Added) != 2 || result.Retained != 1 || !reflect.DeepEqual(result.Removed, []string{"cursor/paid"}) {
		t.Fatalf("merge failed: %+v %v", result, err)
	}
	doc, providers, _ := piDoc(candidate)
	if doc["large"] != json.Number("9007199254740993") || providers["other"].(object)["apiKey"] != "keep" {
		t.Fatal("unrelated fields were changed")
	}
	cur, _ := piModels(providers["agent2api-cursor"].(object))
	if cur["cursor/free"]["name"] != "My label" || cur["cursor/free"]["reasoning"] != false || cur["cursor/free"]["compat"].(object)["requiresToolResultName"] != true {
		t.Fatal("model preferences or fixed thinking were lost")
	}
	or, _ := piModels(providers["agent2api-openrouter"].(object))
	if or["openrouter/free"]["maxTokens"] != json.Number("8192") {
		t.Fatal("OpenRouter output cap missing")
	}
	repeat, result, err := PreparePi(candidate, models, []string{"cursor", "openrouter"}, "http://127.0.0.1:8787", "", now)
	if err != nil || result.Status != "unchanged" || string(repeat) != string(candidate) {
		t.Fatal("repeat sync was not a no-op", err)
	}
}

func TestPiRejectsUnavailableModelsConflictsAndInvalidDocuments(t *testing.T) {
	now := time.Now().UTC()
	m := free("workbuddy", "free", now)
	for _, raw := range []string{"", "null", "{} {}", `{"providers":[]}`, `{"providers":{"agent2api-workbuddy":{"api":"openai-completions","baseUrl":"https://foreign.example/v1"}}}`, `{"providers":{"agent2api-workbuddy":{"api":"openai-completions","baseUrl":"http://localhost:8787/v1","models":[{"id":"x"},{"id":"x"}]}}}`} {
		if _, _, err := PreparePi([]byte(raw), []Model{m}, []string{"workbuddy"}, "http://127.0.0.1:8787", "", now); err == nil {
			t.Fatal("invalid config accepted", raw)
		}
	}
	for _, platform := range []string{"unknown", "openrouter"} {
		if _, _, err := PreparePi(nil, []Model{m}, []string{platform}, "http://127.0.0.1:8787", "", now); err == nil {
			t.Fatal("unavailable platform accepted")
		}
	}
	m.Pricing.Status = "dynamic"
	if _, _, err := PreparePi(nil, []Model{m}, []string{"workbuddy"}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("dynamic model accepted")
	}
}

func TestCodingClientsExcludeFreeModelsWithoutTools(t *testing.T) {
	now := time.Now().UTC()
	good, unsuitable := free("workbuddy", "coding", now), free("workbuddy", "chat-only", now)
	unsuitable.SupportsTools = false
	_, result, err := PreparePi(nil, []Model{good, unsuitable}, []string{"workbuddy"}, "http://127.0.0.1:8787", "", now)
	if err != nil || !reflect.DeepEqual(result.ModelIDs, []string{"workbuddy/coding"}) {
		t.Fatal("chat-only model was imported into a coding client", result, err)
	}
	if _, err := selected([]Model{unsuitable}, []string{"workbuddy"}, now); err == nil {
		t.Fatal("unsupported-only platform accepted")
	}
}

func TestPiStoreCreatesOnceBacksUpAndRejectsConcurrentEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "models.json")
	if _, err := WriteFile(path, nil, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	backup, err := WriteFile(path, []byte("{}"), []byte("{}"))
	after, _ := os.Stat(path)
	if err != nil || backup != "" || !stat.ModTime().Equal(after.ModTime()) {
		t.Fatal("no-op wrote a file")
	}
	if _, err = WriteFile(path, nil, []byte("overwrite")); err == nil {
		t.Fatal("first-create race overwrote existing file")
	}
	backup, err = WriteFile(path, []byte("{}"), []byte("{\"new\":true}"))
	raw, _ := os.ReadFile(backup)
	if err != nil || string(raw) != "{}" {
		t.Fatal("exact backup missing", err)
	}
	if _, err = WriteFile(path, []byte("{}"), []byte("overwrite")); err == nil {
		t.Fatal("concurrent edit was overwritten")
	}
	os.WriteFile(path+".agent2api.lock", []byte("other writer"), 0o600)
	if _, err = WriteFile(path, []byte("{\"new\":true}"), []byte("overwrite")); err == nil {
		t.Fatal("foreign writer lock ignored")
	}
}

func ccFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "配置 目录 #")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "cc-switch.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE providers(id TEXT NOT NULL,app_type TEXT NOT NULL,name TEXT NOT NULL,settings_config TEXT NOT NULL,meta TEXT NOT NULL DEFAULT '{}',is_current BOOLEAN NOT NULL DEFAULT 0,in_failover_queue BOOLEAN NOT NULL DEFAULT 0,created_at INTEGER,sort_index INTEGER,category TEXT,notes TEXT,unknown_preference TEXT DEFAULT 'keep',PRIMARY KEY(id,app_type));
CREATE TABLE provider_endpoints(id INTEGER PRIMARY KEY,provider_id TEXT,app_type TEXT,url TEXT);
CREATE TABLE history(message TEXT);
INSERT INTO history VALUES('do not alter');
INSERT INTO providers(id,app_type,name,settings_config,is_current,sort_index) VALUES('other','claude','Other','{"env":{"keep":"secret"}}',1,7);
INSERT INTO provider_endpoints VALUES(1,'other','claude','https://other.example');`)
	if err != nil {
		t.Fatal(err)
	}
	return path, db
}

func TestCCSwitchTransactionBackupAndRepeatedSync(t *testing.T) {
	path, db := ccFixture(t)
	now, ctx := time.Now().UTC(), context.Background()
	models := []Model{free("workbuddy", "a", now), free("openrouter", "b", now)}
	snapshot, err := ReadCC(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy", "openrouter"}, []string{"claude", "codex"}, "http://127.0.0.1:8787", "", now)
	if err != nil || result.ProviderCount != 4 || result.ModelCount != 2 || len(result.Added) != 4 || result.BackupPath == "" {
		t.Fatalf("sync failed: %+v %v", result, err)
	}
	backup, err := sql.Open("sqlite", result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var count int
	backup.QueryRow("SELECT count(*) FROM providers").Scan(&count)
	if count != 1 {
		t.Fatal("backup omitted WAL data or contained new rows")
	}
	var value string
	db.QueryRow("SELECT message FROM history").Scan(&value)
	if value != "do not alter" {
		t.Fatal("history changed")
	}
	db.QueryRow("SELECT settings_config FROM providers WHERE id='other'").Scan(&value)
	if value != `{"env":{"keep":"secret"}}` {
		t.Fatal("foreign provider changed")
	}
	snapshot, _ = ReadCC(ctx, path)
	for _, row := range snapshot {
		cfg, _ := parse([]byte(row.Config))
		if row.App == "codex" {
			var doc object
			if toml.Unmarshal([]byte(cfg["config"].(string)), &doc) != nil || doc["model_provider"] != "agent2api" {
				t.Fatal("invalid Codex configuration")
			}
			for _, unsupported := range []string{"model_max_output_tokens", "disable_response_storage"} {
				if _, exists := doc[unsupported]; exists {
					t.Fatal("generated an unsupported Codex setting", unsupported)
				}
			}
			providers := doc["model_providers"].(object)
			provider := providers["agent2api"].(object)
			if provider["wire_api"] != "responses" || provider["supports_websockets"] != false || provider["base_url"] != "http://127.0.0.1:8787/v1" {
				t.Fatal("Codex transport must use the gateway's HTTP Responses endpoint")
			}
		} else {
			env := cfg["env"].(object)
			if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8787" {
				t.Fatal("Claude URL must be gateway origin")
			}
			for _, field := range []string{"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
				if env[field] != env["ANTHROPIC_MODEL"] {
					t.Fatal("an alias could escape the selected free model", field)
				}
			}
		}
	}
	result, err = SyncCC(ctx, path, snapshot, models, []string{"workbuddy", "openrouter"}, []string{"claude", "codex"}, "http://127.0.0.1:8787", "", now)
	if err != nil || result.Status != "unchanged" || result.BackupPath != "" {
		t.Fatal("repeat sync wrote configuration", result, err)
	}
	files, _ := filepath.Glob(path + ".bak-*")
	if len(files) != 1 {
		t.Fatal("repeat sync created a redundant backup")
	}
}

func TestCCSwitchPreservesEditsAndRejectsActiveRemoval(t *testing.T) {
	path, db := ccFixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	models := []Model{free("workbuddy", "a", now), free("workbuddy", "b", now), free("cursor", "c", now), free("workbuddy-global", "global", now)}
	snapshot, _ := ReadCC(ctx, path)
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy", "cursor", "workbuddy-global"}, []string{"claude"}, "http://127.0.0.1:8787", "", now); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = ReadCC(ctx, path)
	for _, row := range snapshot {
		if strings.Contains(row.ID, "workbuddy") {
			db.Exec("UPDATE providers SET is_current=1 WHERE id=?", row.ID)
			break
		}
	}
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude"}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("concurrent edit ignored")
	}
	snapshot, _ = ReadCC(ctx, path)
	for _, row := range snapshot {
		if row.Current {
			meta, _, _ := ccOwned(row)
			route := meta["agent2api"].(object)["route"]
			for i := range models {
				if Route(models[i]) == route {
					models[i].Pricing.Status = "credits"
				}
			}
		}
	}
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude"}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("active provider was removed")
	}
	db.Exec("UPDATE providers SET is_current=0 WHERE id LIKE 'agent2api-free-%'")
	snapshot, _ = ReadCC(ctx, path)
	result, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude"}, "http://127.0.0.1:8787", "", now)
	if err != nil || len(result.Removed) != 1 {
		t.Fatal("inactive paid entry was not removed", result, err)
	}
	var cursor int
	db.QueryRow("SELECT count(*) FROM providers WHERE id LIKE 'agent2api-free-cursor-%'").Scan(&cursor)
	if cursor != 1 {
		t.Fatal("unselected platform changed")
	}
	var international int
	db.QueryRow("SELECT count(*) FROM providers WHERE id LIKE 'agent2api-free-workbuddy-global-%'").Scan(&international)
	if international != 1 {
		t.Fatal("domestic sync changed an unselected international provider")
	}
}

func TestCCSwitchKeepsUserFieldsAfterNativeEditorDropsUnknownMetadata(t *testing.T) {
	path, db := ccFixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	models := []Model{free("workbuddy", "a", now)}
	snapshot, _ := ReadCC(ctx, path)
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude", "codex"}, "http://127.0.0.1:8787", "", now); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = ReadCC(ctx, path)
	for _, row := range snapshot {
		doc, _ := parse([]byte(row.Config))
		if row.App == "claude" {
			doc["env"].(object)["USER_PREFERENCE"] = "keep"
		} else {
			doc["config"] = "approval_policy = 'on-request'\n" + doc["config"].(string)
		}
		raw, _ := json.Marshal(doc)
		db.Exec("UPDATE providers SET settings_config=?,meta='{}' WHERE id=? AND app_type=?", string(raw), row.ID, row.App)
	}
	snapshot, _ = ReadCC(ctx, path)
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude", "codex"}, "http://127.0.0.1:8787", "", now); err != nil {
		t.Fatal("native edit could not be merged", err)
	}
	snapshot, _ = ReadCC(ctx, path)
	for _, row := range snapshot {
		doc, _ := parse([]byte(row.Config))
		if row.App == "claude" && doc["env"].(object)["USER_PREFERENCE"] != "keep" {
			t.Fatal("user environment preference removed")
		}
		if row.App == "codex" && !strings.Contains(doc["config"].(string), "on-request") {
			t.Fatal("user TOML preference removed")
		}
	}
}

func TestCCSwitchRollsBackOnInsertFailureAndRefusesNameCollision(t *testing.T) {
	path, db := ccFixture(t)
	ctx, now := context.Background(), time.Now().UTC()
	models := []Model{free("workbuddy", "a", now), free("workbuddy", "b", now)}
	snapshot, _ := ReadCC(ctx, path)
	db.Exec(`CREATE TRIGGER reject_insert BEFORE INSERT ON providers WHEN NEW.id LIKE 'agent2api-free-%' BEGIN SELECT RAISE(ABORT,'fixture'); END`)
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude"}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("failed insert was accepted")
	}
	current, _ := ReadCC(ctx, path)
	if len(current) != 0 {
		t.Fatal("failed transaction left partial providers")
	}
	db.Exec("DROP TRIGGER reject_insert")
	db.Exec("INSERT INTO providers(id,app_type,name,settings_config) VALUES(?,'claude','collision','{}')", ccID(models[0]))
	snapshot, _ = ReadCC(ctx, path)
	if _, err := SyncCC(ctx, path, snapshot, models, []string{"workbuddy"}, []string{"claude"}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("foreign row with reserved ID overwritten")
	}
}
