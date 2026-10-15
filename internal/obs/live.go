package obs

import (
	"sort"
	"time"
)

type liveSample struct {
	second     int64
	characters int64
}
type liveRequest struct {
	ID                               uint64
	Protocol, Platform, Model        string
	Started, FirstOutput, LastOutput time.Time
	Characters                       int64
	Samples                          []liveSample
}
type TokenCalibration struct {
	Characters int64 `json:"characters"`
	Tokens     int64 `json:"tokens"`
	Samples    int64 `json:"samples"`
}
type LiveRequest struct {
	ID                       uint64     `json:"id"`
	Protocol                 string     `json:"protocol"`
	Platform                 string     `json:"platform"`
	Model                    string     `json:"model"`
	StartedAt                time.Time  `json:"started_at"`
	LastOutputAt             *time.Time `json:"last_output_at,omitempty"`
	OutputCharacters         int64      `json:"output_characters"`
	CharactersPerSecond      float64    `json:"characters_per_second"`
	EstimatedOutputTokens    *int64     `json:"estimated_output_tokens,omitempty"`
	EstimatedTokensPerSecond *float64   `json:"estimated_tokens_per_second,omitempty"`
}
type CompletedGeneration struct {
	Time         time.Time `json:"time"`
	Platform     string    `json:"platform"`
	Model        string    `json:"model"`
	OutputTokens int64     `json:"output_tokens"`
	GenMs        int64     `json:"gen_ms"`
}
type LiveSnapshot struct {
	SampledAt     time.Time            `json:"sampled_at"`
	InFlight      int64                `json:"in_flight"`
	Active        []LiveRequest        `json:"active"`
	LastCompleted *CompletedGeneration `json:"last_completed,omitempty"`
}

// BeginLive tracks counters only, never prompt, output text or credentials.
func (m *Metrics) BeginLive(protocol string, started time.Time) uint64 {
	m.Begin()
	id := m.liveSequence.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live[id] = &liveRequest{ID: id, Protocol: protocol, Started: started}
	return id
}
func (m *Metrics) RouteLive(id uint64, platform, model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if request := m.live[id]; request != nil {
		request.Platform = platform
		request.Model = model
	}
}
func (m *Metrics) ObserveLive(id uint64, characters int64, now time.Time) {
	if characters <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	request := m.live[id]
	if request == nil {
		return
	}
	request.Characters += characters
	request.LastOutput = now
	if request.FirstOutput.IsZero() {
		request.FirstOutput = now
	}
	second := now.Unix()
	if n := len(request.Samples); n > 0 && request.Samples[n-1].second == second {
		request.Samples[n-1].characters += characters
	} else {
		request.Samples = append(request.Samples, liveSample{second, characters})
	}
	for len(request.Samples) > 0 && request.Samples[0].second < second-3 {
		request.Samples = request.Samples[1:]
	}
}
func calibrationKey(platform, model string) string { return platform + "\x1f" + model }
func (m *Metrics) LiveSnapshot() LiveSnapshot      { return m.liveSnapshotAt(time.Now()) }
func (m *Metrics) liveSnapshotAt(now time.Time) LiveSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := LiveSnapshot{SampledAt: now, InFlight: m.inFlight.Load(), Active: []LiveRequest{}}
	for _, request := range m.live {
		row := LiveRequest{ID: request.ID, Platform: request.Platform, Model: request.Model, Protocol: request.Protocol, StartedAt: request.Started, OutputCharacters: request.Characters}
		if !request.LastOutput.IsZero() {
			stamp := request.LastOutput
			row.LastOutputAt = &stamp
		}
		var chars int64
		for _, sample := range request.Samples {
			if sample.second >= now.Unix()-2 {
				chars += sample.characters
			}
		}
		seconds := 3.0
		if elapsed := now.Sub(request.Started).Seconds(); elapsed < seconds {
			seconds = elapsed
		}
		if seconds >= .001 {
			row.CharactersPerSecond = float64(chars) / seconds
		}
		if cal := m.calibrations[calibrationKey(row.Platform, row.Model)]; cal.Characters > 0 && cal.Tokens > 0 {
			estimate := int64(float64(row.OutputCharacters)*float64(cal.Tokens)/float64(cal.Characters) + .5)
			speed := row.CharactersPerSecond * float64(cal.Tokens) / float64(cal.Characters)
			row.EstimatedOutputTokens = &estimate
			row.EstimatedTokensPerSecond = &speed
		}
		out.Active = append(out.Active, row)
	}
	sort.Slice(out.Active, func(i, j int) bool { return out.Active[i].ID < out.Active[j].ID })
	for i := len(m.recent) - 1; i >= 0; i-- {
		r := m.recent[i]
		if r.OutputTokens > 0 && r.GenMs > 0 {
			out.LastCompleted = &CompletedGeneration{r.Time, r.Platform, r.Model, r.OutputTokens, r.GenMs}
			break
		}
	}
	return out
}
