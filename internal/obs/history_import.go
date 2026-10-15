package obs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type HistorySource struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	ImportedRequests int64  `json:"imported_requests"`
	SnapshotRequests int64  `json:"snapshot_requests,omitempty"`
	SnapshotInput    int64  `json:"snapshot_input,omitempty"`
	SnapshotOutput   int64  `json:"snapshot_output,omitempty"`
}

func recordIdentity(r Record) string {
	raw, _ := json.Marshal([]any{r.Time.UTC(), r.Platform, r.Model, r.Protocol, r.Stream, r.Account, r.DurationMs, r.Status, r.OK})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

var logTimePattern = regexp.MustCompile(`\[agent2api\] (\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})(?:\.\d+)? `)

// ImportHistory reads a local archived snapshot or completion log. It never
// alters lifetime totals, the recent buffer, credentials, or the source file.
// Exact historical records and source hashes survive Save/Load for idempotency.
func (m *Metrics) ImportHistory(path string, location *time.Location) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return fmt.Errorf("history source exceeds 32MB")
	}
	digest := sha256.Sum256(raw)
	id := hex.EncodeToString(digest[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, source := range m.historySources {
		if source.ID == id {
			return nil
		}
	}
	source := HistorySource{ID: id, Name: filepath.Base(path), Kind: "snapshot"}
	if strings.EqualFold(filepath.Ext(path), ".log") {
		if location == nil {
			return fmt.Errorf("log timezone must be explicitly configured")
		}
		source.Kind = "completion_log"
		// Do not combine timestamp-only log rows with snapshot rows from the same
		// day: logs lack unique IDs/tokens, so that could count a request twice.
		exactDates := map[string]bool{}
		for date, models := range m.daily {
			for _, model := range models {
				if model.Total > model.UnknownTokenRequests {
					exactDates[date] = true
				}
			}
		}
		scanner := bufio.NewScanner(strings.NewReader(string(raw)))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.Contains(line, "protocol=") {
				continue
			}
			stamp := logTimePattern.FindStringSubmatch(line)
			if len(stamp) < 2 {
				continue
			}
			when, err := time.ParseInLocation("2006/01/02 15:04:05", stamp[1], location)
			if err != nil {
				continue
			}
			if !when.Before(m.trackingSince) || exactDates[when.UTC().Format(time.DateOnly)] {
				continue
			}
			fields := map[string]string{}
			for _, field := range strings.Fields(line) {
				if key, value, ok := strings.Cut(field, "="); ok {
					fields[key] = value
				}
			}
			status, _ := strconv.Atoi(fields["status"])
			duration, _ := strconv.ParseInt(fields["dur_ms"], 10, 64)
			if fields["protocol"] == "" || fields["model"] == "" {
				continue
			}
			hash := sha256.Sum256([]byte(line))
			key := "log:" + hex.EncodeToString(hash[:])
			if m.historyIDs[key] {
				continue
			}
			record := Record{Time: when, Platform: fields["platform"], Model: fields["model"], Protocol: fields["protocol"], Stream: fields["stream"] == "true", Status: status, OK: fields["ok"] == "true", DurationMs: duration}
			if !m.historyDateAllowed(when) {
				continue
			}
			m.recordHistory(record)
			m.daily[when.UTC().Format(time.DateOnly)][usageKey{record.Platform, record.Model}].UnknownTokenRequests++
			m.historyIDs[key] = true
			source.ImportedRequests++
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	} else {
		var snapshot persisted
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		source.SnapshotRequests = snapshot.Total
		source.SnapshotInput = snapshot.InputTok
		source.SnapshotOutput = snapshot.OutputTok
		for _, record := range snapshot.Recent {
			if !record.Time.Before(m.trackingSince) || !m.historyDateAllowed(record.Time) {
				continue
			}
			key := recordIdentity(record)
			if m.historyIDs[key] {
				continue
			}
			m.recordHistory(record)
			m.historyIDs[key] = true
			source.ImportedRequests++
		}
	}
	m.historySources = append(m.historySources, source)
	return nil
}
func (m *Metrics) historyDateAllowed(when time.Time) bool {
	return !when.IsZero() && when.UTC().Format(time.DateOnly) >= time.Now().UTC().AddDate(0, 0, 1-historyDays).Format(time.DateOnly)
}
