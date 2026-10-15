package obs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImportHistoryPreservesTotalsAndDeduplicatesAcrossRestarts(t *testing.T) {
	m := New()
	now := time.Now()
	m.Record(Record{Time: now, Model: "current", OK: true, InputTokens: 3})
	r := Record{Time: now.AddDate(0, 0, -7), Model: "archived", Platform: "p", Protocol: "chat", OK: true, InputTokens: 100, OutputTokens: 20}
	raw, _ := json.Marshal(persisted{Total: 99999, InputTok: 999999, Recent: []Record{r, r}})
	dir := t.TempDir()
	source := filepath.Join(dir, "archive.json")
	_ = os.WriteFile(source, raw, 0600)
	if err := m.ImportHistory(source, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot()
	if snapshot.Total != 1 || snapshot.InputTokens != 3 {
		t.Fatal("archive lifetime totals were incorrectly added")
	}
	if len(snapshot.UsageHistory.Days) != 2 || snapshot.UsageHistory.Days[0].Models[0].Total != 1 {
		t.Fatalf("bad recovered dates: %+v", snapshot.UsageHistory)
	}
	if snapshot.UsageHistory.Sources[0].ImportedRequests != 1 || snapshot.UsageHistory.Sources[0].SnapshotRequests != 99999 {
		t.Fatal("lost source provenance")
	}
	saved := filepath.Join(dir, "metrics.json")
	if err := m.Save(saved); err != nil {
		t.Fatal(err)
	}
	again := New()
	if err := again.Load(saved); err != nil {
		t.Fatal(err)
	}
	if err := again.ImportHistory(source, nil); err != nil {
		t.Fatal(err)
	}
	// A different archive file with overlapping records must not double count.
	copySource := filepath.Join(dir, "overlap.json")
	raw, _ = json.Marshal(persisted{Total: 100000, Recent: []Record{r}})
	_ = os.WriteFile(copySource, raw, 0600)
	if err := again.ImportHistory(copySource, nil); err != nil {
		t.Fatal(err)
	}
	if got := again.Snapshot().UsageHistory.Days[0].Models[0].Total; got != 1 {
		t.Fatalf("archive duplicated: %d", got)
	}
}
func TestLogImportRequiresTimezoneAndDoesNotInventTokens(t *testing.T) {
	m := New()
	dir := t.TempDir()
	file := filepath.Join(dir, "history.log")
	day := time.Now().UTC().AddDate(0, 0, -6).Format("2006/01/02")
	line := fmt.Sprintf("[agent2api] %s 12:00:00.123456 protocol=openai-chat platform=p model=m stream=true dur_ms=123 ok=true status=200", day)
	_ = os.WriteFile(file, []byte(line+"\n"+line+"\nnot a request\n"), 0600)
	if err := m.ImportHistory(file, nil); err == nil {
		t.Fatal("log timezone must be explicit")
	}
	if err := m.ImportHistory(file, time.FixedZone("UTC+8", 8*3600)); err != nil {
		t.Fatal(err)
	}
	history := m.Snapshot().UsageHistory
	row := history.Days[0].Models[0]
	if row.Total != 1 || row.UnknownTokenRequests != 1 || row.InputTokens != 0 || row.OutputTokens != 0 {
		t.Fatalf("log tokens invented: %+v", row)
	}
	if m.Snapshot().Total != 0 {
		t.Fatal("log imported into lifetime counters")
	}
	if err := m.ImportHistory(file, time.UTC); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot().UsageHistory.Days[0].Models[0].Total; got != 1 {
		t.Fatal("same source imported twice")
	}
}

func TestModernArchiveFillsMissingDaysAfterTrackingStartWithoutOverlappingLiveDay(t *testing.T) {
	m := New()
	m.trackingSince = time.Now().UTC().AddDate(0, 0, -10)
	today := time.Now().UTC().Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	m.Record(Record{Time: time.Now(), Platform: "p", Model: "m", OK: true})
	row := UsageModel{Platform: "p", Model: "m", Total: 500, OK: 500, InputTokens: 900}
	raw, _ := json.Marshal(persisted{Total: 9999, UsageHistory: &UsageHistory{Timezone: "UTC", Days: []UsageDay{{Date: yesterday, Models: []UsageModel{row}}, {Date: today, Models: []UsageModel{row}}}}})
	file := filepath.Join(t.TempDir(), "archive.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.ImportHistory(file, nil); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot()
	if s.Total != 1 || len(s.UsageHistory.Days) != 2 || s.UsageHistory.Days[0].Models[0].Total != 500 || s.UsageHistory.Days[1].Models[0].Total != 1 {
		t.Fatalf("missing history after tracking start, or live day overwritten: %+v", s.UsageHistory)
	}
}
