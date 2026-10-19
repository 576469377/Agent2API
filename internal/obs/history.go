package obs

import (
	"sort"
	"time"
)

// Calendar history is independent of the bounded recent-request buffer.
// UTC is explicit so servers and clients agree on day boundaries across DST.
const historyDays = 366

type UsageModel struct {
	UnknownTokenRequests int64  `json:"unknown_token_requests,omitempty"`
	Platform             string `json:"platform"`
	Model                string `json:"model"`
	Total                int64  `json:"total"`
	OK                   int64  `json:"ok"`
	Failed               int64  `json:"failed"`
	InputTokens          int64  `json:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	DurationSum          int64  `json:"duration_sum"`
}

type UsageDay struct {
	Date   string       `json:"date"`
	Models []UsageModel `json:"models"`
}

type UsageHistory struct {
	Sources        []HistorySource `json:"sources,omitempty"`
	RecordIDs      []string        `json:"-"`
	Timezone       string          `json:"timezone"`
	TrackingSince  time.Time       `json:"tracking_since"`
	LegacyRequests int64           `json:"legacy_requests"`
	RetentionDays  int             `json:"retention_days"`
	Days           []UsageDay      `json:"days"`
}

type usageKey struct{ platform, model string }

// Caller holds m.mu. Record timestamps are request-start timestamps, consistent
// with the existing minute series; counts appear only after requests complete.
func (m *Metrics) recordHistory(r Record) {
	if r.Time.IsZero() {
		return
	}
	date := r.Time.UTC().Format(time.DateOnly)
	if date < time.Now().UTC().AddDate(0, 0, 1-historyDays).Format(time.DateOnly) {
		return
	}
	models := m.daily[date]
	if models == nil {
		models = make(map[usageKey]*UsageModel)
		m.daily[date] = models
	}
	key := usageKey{r.Platform, r.Model}
	c := models[key]
	if c == nil {
		c = &UsageModel{Platform: r.Platform, Model: r.Model}
		models[key] = c
	}
	c.Total++
	if r.OK {
		c.OK++
	} else {
		c.Failed++
	}
	c.InputTokens += r.InputTokens
	c.OutputTokens += r.OutputTokens
	if r.OutputCharacters > 0 && !r.UsageReported && r.InputTokens == 0 && r.OutputTokens == 0 {
		c.UnknownTokenRequests++
	}
	c.DurationSum += r.DurationMs
}

// Caller holds m.mu. All values are copied, including each day's model rows.
func (m *Metrics) historySnapshot() UsageHistory {
	out := UsageHistory{Sources: append([]HistorySource(nil), m.historySources...), Timezone: "UTC", TrackingSince: m.trackingSince,
		LegacyRequests: m.legacyRequests, RetentionDays: historyDays, Days: []UsageDay{}}
	cutoff := time.Now().UTC().AddDate(0, 0, 1-historyDays).Format(time.DateOnly)
	for date, models := range m.daily {
		if date < cutoff {
			delete(m.daily, date)
			continue
		}
		day := UsageDay{Date: date, Models: make([]UsageModel, 0, len(models))}
		for _, c := range models {
			day.Models = append(day.Models, *c)
		}
		sort.Slice(day.Models, func(i, j int) bool {
			a, b := day.Models[i], day.Models[j]
			if a.Platform != b.Platform {
				return a.Platform < b.Platform
			}
			return a.Model < b.Model
		})
		out.Days = append(out.Days, day)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Date < out.Days[j].Date })
	return out
}

// Legacy files only contain the latest 200 requests. Preserve those actual
// observations once, but never invent dates for undated lifetime aggregates.
func (m *Metrics) loadHistory(history *UsageHistory, recent []Record, total int64) {
	m.daily = make(map[string]map[usageKey]*UsageModel)
	m.historyIDs = make(map[string]bool)
	m.historySources = nil
	if history == nil {
		m.trackingSince = time.Now().UTC()
		m.legacyRequests = total
		for _, r := range recent {
			m.recordHistory(r)
			m.historyIDs[recordIdentity(r)] = true
		}
		return
	}
	m.historySources = append([]HistorySource(nil), history.Sources...)
	for _, key := range history.RecordIDs {
		m.historyIDs[key] = true
	}
	m.trackingSince = history.TrackingSince
	if m.trackingSince.IsZero() {
		m.trackingSince = time.Now().UTC()
	}
	m.legacyRequests = history.LegacyRequests
	for _, day := range history.Days {
		if _, err := time.Parse(time.DateOnly, day.Date); err != nil {
			continue
		}
		models := make(map[usageKey]*UsageModel)
		for _, row := range day.Models {
			c := row
			models[usageKey{c.Platform, c.Model}] = &c
		}
		m.daily[day.Date] = models
	}
	m.historySnapshot() // prune expired persisted days
}
