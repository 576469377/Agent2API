// Package obs 提供网关运行指标采集。
//
// 设计取向：全部走原子计数器 + 一把互斥锁保护聚合结构，热路径无阻塞，
// 也不引入 Prometheus 这类外部依赖——同量级的自托管网关用 JSON 快照直接喂
// 给内置控制台就够了。
package obs

import (
	"encoding/json"
	"github.com/576469377/Agent2API/internal/llm"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// recentCap 是最近请求日志的环形缓冲容量。
const recentCap = 200

// seriesCap 保留最近多少个分钟桶（存储上限，应大于展示窗口）。
const seriesCap = 60

// seriesWindow 是趋势图展示的连续分钟窗口。
//
// 即使某些分钟没有任何请求，也会用零值桶补齐，使趋势图呈现实心时间轴，
// 而不是在稀疏流量下显得空荡。
const seriesWindow = 30

// Record 是一次请求的观测记录。
type Record struct {
	RequestID        string        `json:"request_id,omitempty"`
	AttemptCount     int           `json:"attempt_count,omitempty"`
	QueueMs          int64         `json:"queue_ms,omitempty"`
	Attempts         []llm.Attempt `json:"attempts,omitempty"`
	LiveID           uint64        `json:"-"`
	OutputCharacters int64         `json:"output_characters,omitempty"`
	FirstOutputMs    *int64        `json:"first_output_ms,omitempty"`
	UsageReported    bool          `json:"usage_reported,omitempty"`
	Time             time.Time     `json:"time"`
	Protocol         string        `json:"protocol"`
	Model            string        `json:"model"`
	Stream           bool          `json:"stream"`
	OK               bool          `json:"ok"`
	Status           int           `json:"status"`
	DurationMs       int64         `json:"duration_ms"`
	InputTokens      int64         `json:"input_tokens,omitempty"`
	OutputTokens     int64         `json:"output_tokens,omitempty"`
	ReasoningTokens  int64         `json:"reasoning_tokens,omitempty"`
	CachedTokens     int64         `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64         `json:"cache_write_tokens,omitempty"`
	Error            string        `json:"error,omitempty"`
	ErrorCode        string        `json:"error_code,omitempty"`
	// Pointers distinguish newly measured zero from legacy records with no count.
	ImageCount          *int   `json:"image_count,omitempty"`
	MaxImages           *int   `json:"max_images,omitempty"` // Effective guard; 0 means explicitly disabled.
	ForwardedImageCount *int   `json:"forwarded_image_count,omitempty"`
	RemovedImageCount   int    `json:"removed_image_count,omitempty"`
	ImageOverflowPolicy string `json:"image_overflow_policy,omitempty"`

	// Account 是本次请求实际使用的上游账号标识（号池的凭证文件名）。
	// 单账号模式为空。它是「每个账号用了多少额度」的唯一归因来源——
	// 没有它，多账号场景下控制台只能看到总量，看不出账号间的分布。
	Account string `json:"account,omitempty"`

	// Platform 是本次请求实际路由到的上游平台 ID（多平台集成时的归因字段）。
	Platform string `json:"platform,omitempty"`

	// ModelRequested 是客户端**请求里写的**模型名，仅当它与 Model（实际发往
	// 上游的名字）不同时才记录——即命中了 models.aliases 的别名。
	// 控制台据此回答「这条 claude-sonnet-4-5 的请求实际用了哪个模型」。
	ModelRequested string `json:"model_requested,omitempty"`

	// GenMs 是网关取得上游流后，到用量结束的接收耗时（毫秒）。
	// 含流内网络等待及客户端背压，不代表模型内部解码耗时。
	// 它是 TPS 的分母，与 DurationMs（含建连、解码、写回）语义不同。
	// 0 表示**未测量**（失败请求、未产出 token、或来自无该字段的旧指标文件），
	// 因此取值时必须把 0 当作「无数据」而不是「0 毫秒」——否则会算出 +Inf。
	GenMs int64 `json:"gen_ms,omitempty"`
}

// groupCounter 是按某个维度（模型、协议）聚合的计数器。
type groupCounter struct {
	Total        int64 `json:"total"`
	OK           int64 `json:"ok"`
	Failed       int64 `json:"failed"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	DurationSum  int64 `json:"-"`
}

// minuteBucket 是一分钟的聚合桶，用于画趋势图。
type minuteBucket struct {
	Minute       int64 `json:"minute"`
	Total        int64 `json:"total"`
	OK           int64 `json:"ok"`
	Failed       int64 `json:"failed"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`

	// GenMs / GenTok 是该分钟内「测到生成耗时」的那批请求的生成毫秒数与输出 token 数。
	// 二者一起构成该分钟的 TPS 分子分母；无数据的分钟两者均为 0，
	// 消费方必须据此显示「无数据」而非 0 tok/s。
	GenMs  int64 `json:"gen_ms"`
	GenTok int64 `json:"gen_tok"`
}

// Metrics 采集网关运行指标。
type Metrics struct {
	started time.Time

	total    atomic.Int64
	okCount  atomic.Int64
	failed   atomic.Int64
	inFlight atomic.Int64

	inputTok  atomic.Int64
	outputTok atomic.Int64
	reasonTok atomic.Int64
	durSum    atomic.Int64

	// 解码速度（TPS）的三元组，只累加「测到生成耗时」的请求。
	//
	// 分子刻意用 genTokSum 而不是上面的 outputTok：这样分子与分母必然来自
	// 同一批请求。分母用 genCount 而不是 total，因为失败请求与未测到耗时的请求
	// 不该摊薄平均速度（对照 AvgLatencyMs 用 total 做分母，语义不同）。
	genMsSum  atomic.Int64
	genTokSum atomic.Int64
	genCount  atomic.Int64

	liveSequence   atomic.Uint64
	savedAt        atomic.Int64
	live           map[uint64]*liveRequest
	requestStarts  [60]requestRateBucket
	calibrations   map[string]TokenCalibration
	mu             sync.Mutex
	byModel        map[string]*groupCounter
	byProto        map[string]*groupCounter
	byAccount      map[string]*groupCounter
	recent         []Record
	series         []minuteBucket
	daily          map[string]map[usageKey]*UsageModel
	historyIDs     map[string]bool
	historySources []HistorySource
	trackingSince  time.Time
	legacyRequests int64
}

// New 创建指标采集器。
func New() *Metrics {
	return &Metrics{
		started:       time.Now(),
		live:          make(map[uint64]*liveRequest),
		calibrations:  make(map[string]TokenCalibration),
		byModel:       map[string]*groupCounter{},
		byProto:       map[string]*groupCounter{},
		byAccount:     map[string]*groupCounter{},
		recent:        make([]Record, 0, recentCap),
		series:        make([]minuteBucket, 0, seriesCap),
		daily:         make(map[string]map[usageKey]*UsageModel),
		historyIDs:    make(map[string]bool),
		trackingSince: time.Now().UTC(),
	}
}

// Begin 标记一个请求开始，返回后必须配对调用 Record。
func (m *Metrics) Begin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beginLocked(time.Now())
}

// Abandon 标记一个请求异常终止（未走正常 Record 路径）。
func (m *Metrics) Abandon() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v := m.inFlight.Add(-1); v < 0 {
		m.inFlight.Store(0)
	}
}

// Record 记录一次已完成的请求。
func (m *Metrics) Record(r Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v := m.inFlight.Add(-1); v < 0 {
		m.inFlight.Store(0)
	}

	m.total.Add(1)
	if r.OK {
		m.okCount.Add(1)
	} else {
		m.failed.Add(1)
	}
	m.inputTok.Add(r.InputTokens)
	m.outputTok.Add(r.OutputTokens)
	m.reasonTok.Add(r.ReasoningTokens)
	m.durSum.Add(r.DurationMs)
	// GenMs > 0 才计入 TPS 三元组：0 表示未测量（未产出 token / 旧数据）。
	//
	// 刻意**不**按 OK 过滤：一个中途失败但已产出若干 token 的流，那段解码是
	// 真实发生过的算力，属于有效速度样本。过滤掉它会让 TPS 系统性偏高。
	if r.GenMs > 0 {
		m.genMsSum.Add(r.GenMs)
		m.genTokSum.Add(r.OutputTokens)
		m.genCount.Add(1)
	}

	minute := r.Time.Unix() / 60

	delete(m.live, r.LiveID)
	if r.OK && r.OutputCharacters > 0 && r.OutputTokens > 0 {
		key := calibrationKey(r.Platform, r.Model)
		cal := m.calibrations[key]
		cal.Characters += r.OutputCharacters
		cal.Tokens += r.OutputTokens
		cal.Samples++
		m.calibrations[key] = cal
	}
	m.bump(m.byModel, r.Model, r)
	m.bump(m.byProto, r.Protocol, r)
	m.bump(m.byAccount, r.Account, r)
	m.recordHistory(r)

	// 最近请求（新在前）
	m.recent = append(m.recent, r)
	if len(m.recent) > recentCap {
		m.recent = m.recent[len(m.recent)-recentCap:]
	}

	// Concurrent requests can finish out of start-time order. Merge the actual
	// minute rather than appending duplicate buckets which hide earlier counts.
	i := sort.Search(len(m.series), func(i int) bool { return m.series[i].Minute >= minute })
	if i == len(m.series) || m.series[i].Minute != minute {
		m.series = append(m.series, minuteBucket{})
		copy(m.series[i+1:], m.series[i:])
		m.series[i] = minuteBucket{Minute: minute}
	}
	b := &m.series[i]
	b.Total++
	b.InputTokens += r.InputTokens
	b.OutputTokens += r.OutputTokens
	if r.GenMs > 0 {
		b.GenMs += r.GenMs
		b.GenTok += r.OutputTokens
	}
	if r.OK {
		b.OK++
	} else {
		b.Failed++
	}
	if len(m.series) > seriesCap {
		m.series = m.series[len(m.series)-seriesCap:]
	}
}

func (m *Metrics) bump(store map[string]*groupCounter, key string, r Record) {
	if key == "" {
		key = "(unknown)"
	}
	c, ok := store[key]
	if !ok {
		c = &groupCounter{}
		store[key] = c
	}
	c.Total++
	c.InputTokens += r.InputTokens
	c.OutputTokens += r.OutputTokens
	c.DurationSum += r.DurationMs
	if r.OK {
		c.OK++
	} else {
		c.Failed++
	}
}

// GroupStat 是某个维度下的聚合统计。
type GroupStat struct {
	Key          string  `json:"key"`
	Total        int64   `json:"total"`
	OK           int64   `json:"ok"`
	Failed       int64   `json:"failed"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

// Snapshot 是指标快照，直接序列化给控制台。
type Snapshot struct {
	UptimeSec       int64   `json:"uptime_sec"`
	InFlight        int64   `json:"in_flight"`
	Total           int64   `json:"total"`
	OK              int64   `json:"ok"`
	Failed          int64   `json:"failed"`
	SuccessRate     float64 `json:"success_rate"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	LastMinuteRPM   int64   `json:"last_minute_rpm"`

	// AvgTPS 是平均解码速度：输出 token ÷ 生成秒数（加权平均）。
	// TPSSamples 是参与计算的请求数——为 0 时表示**无可用数据**，
	// 前端必须显示「—」而不是 "0.0 tok/s"（0 与「没测过」是两回事）。
	AvgTPS     float64 `json:"avg_tps"`
	TPSSamples int64   `json:"tps_samples"`

	ByModel      []GroupStat    `json:"by_model"`
	ByProtocol   []GroupStat    `json:"by_protocol"`
	ByAccount    []GroupStat    `json:"by_account"`
	Series       []minuteBucket `json:"series"`
	Recent       []Record       `json:"recent"`
	UsageHistory UsageHistory   `json:"usage_history"`
}

// Snapshot 生成当前快照。
func (m *Metrics) Snapshot() Snapshot {
	// All counters, groups and recent records belong to one completed-request
	// snapshot; reading independent atomics before the lock can mix generations.
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.total.Load()
	ok := m.okCount.Load()
	failed := m.failed.Load()

	avg := 0.0
	if total > 0 {
		avg = float64(m.durSum.Load()) / float64(total)
	}

	// 解码速度：分子分母都取自「测到生成耗时」的那批请求。
	//
	// 守卫 genMs > 0 是必须的，不是防御性冗余：旧版指标文件没有 gen_* 字段，
	// 重启加载后 genMsSum 为 0 而 output_tokens 是个大数，直接相除会得到 +Inf；
	// encoding/json 无法序列化 +Inf（Marshal 直接报错返回空），
	// 结果就是 /api/metrics 返回 500、控制台概览与调用日志两页同时空掉。
	genMs := m.genMsSum.Load()
	genN := m.genCount.Load()
	tps := 0.0
	if genMs > 0 && genN > 0 {
		tps = float64(m.genTokSum.Load()) / (float64(genMs) / 1000.0)
	}

	byModel := toStats(m.byModel)
	byProto := toStats(m.byProto)
	byAccount := toStats(m.byAccount)
	history := m.historySnapshot()
	rawSeries := make([]minuteBucket, len(m.series))
	copy(rawSeries, m.series)
	series := fillSeriesBuckets(rawSeries, seriesWindow)

	recent := make([]Record, len(m.recent))
	for i, r := range m.recent {
		recent[len(m.recent)-1-i] = r // 反转：最新的排最前
	}
	rpm := m.lastMinuteRPMLocked(time.Now())

	return Snapshot{
		UptimeSec:       int64(time.Since(m.started).Seconds()),
		InFlight:        m.inFlight.Load(),
		Total:           total,
		OK:              ok,
		Failed:          failed,
		SuccessRate:     ratio(ok, total),
		AvgLatencyMs:    avg,
		InputTokens:     m.inputTok.Load(),
		OutputTokens:    m.outputTok.Load(),
		ReasoningTokens: m.reasonTok.Load(),
		LastMinuteRPM:   rpm,
		AvgTPS:          tps,
		TPSSamples:      genN,
		ByModel:         byModel,
		ByProtocol:      byProto,
		ByAccount:       byAccount,
		Series:          series,
		Recent:          recent,
		UsageHistory:    history,
	}
}

func toStats(store map[string]*groupCounter) []GroupStat {
	out := make([]GroupStat, 0, len(store))
	for k, c := range store {
		avg := 0.0
		if c.Total > 0 {
			avg = float64(c.DurationSum) / float64(c.Total)
		}
		out = append(out, GroupStat{
			Key:          k,
			Total:        c.Total,
			OK:           c.OK,
			Failed:       c.Failed,
			InputTokens:  c.InputTokens,
			OutputTokens: c.OutputTokens,
			AvgLatencyMs: avg,
			SuccessRate:  ratio(c.OK, c.Total),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// fillSeriesBuckets 把原始的分钟桶补齐成一段连续的展示窗口。
//
// 窗口以「当前分钟」为右端，向左覆盖 window 分钟；原始数据里缺失的分钟
// 用零值桶补齐，minute 字段保留真实分钟戳，供前端绘制时间轴标签。
func fillSeriesBuckets(raw []minuteBucket, window int) []minuteBucket {
	if window < 1 {
		window = 1
	}
	nowMin := time.Now().Unix() / 60
	startMin := nowMin - int64(window) + 1

	idx := indexMinuteBuckets(raw)

	out := make([]minuteBucket, 0, window)
	for m := startMin; m <= nowMin; m++ {
		if b, ok := idx[m]; ok {
			out = append(out, b)
		} else {
			out = append(out, minuteBucket{Minute: m})
		}
	}
	return out
}

func indexMinuteBuckets(raw []minuteBucket) map[int64]minuteBucket {
	idx := make(map[int64]minuteBucket, len(raw))
	for _, b := range raw {
		sum := idx[b.Minute]
		sum.Minute = b.Minute
		sum.Total += b.Total
		sum.OK += b.OK
		sum.Failed += b.Failed
		sum.InputTokens += b.InputTokens
		sum.OutputTokens += b.OutputTokens
		sum.GenMs += b.GenMs
		sum.GenTok += b.GenTok
		idx[b.Minute] = sum
	}
	return idx
}

func normalizeMinuteBuckets(raw []minuteBucket) []minuteBucket {
	idx := indexMinuteBuckets(raw)
	result := make([]minuteBucket, 0, len(idx))
	for _, b := range idx {
		result = append(result, b)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Minute < result[j].Minute })
	if len(result) > seriesCap {
		result = result[len(result)-seriesCap:]
	}
	return result
}

// ───────────────────────── 持久化 ─────────────────────────
//
// 指标默认仅存于内存，进程重启即清零。为让控制台在重启后仍能看到历史统计，
// 这里提供 Save/Load：把累积计数、分组聚合、最近请求与分钟桶序列化到磁盘，
// 启动时被 Load 重建。调用方应记录读写错误并保留读取失败的原文件。

type persistCounter struct {
	Total        int64 `json:"total"`
	OK           int64 `json:"ok"`
	Failed       int64 `json:"failed"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	DurationSum  int64 `json:"duration_sum"`
}

// persisted 是落盘后的指标快照形态，足以重建内存态。
//
// 注意：新增字段必须同时出现在本结构、Save 的字面量与 Load 的 Store 三处，
// 漏掉任何一处都会静默丢数据（旧文件缺键即零值，无需版本迁移）。
// TPS 三元组只做顶层持久化，不进 persistCounter —— 分组维度的 TPS 当前无人消费，
// 而 groupCounter/persistCounter 是逐字段手写拷贝，每加一个字段就多一处漏改风险。
type persisted struct {
	HistoryRecordIDs []string                    `json:"history_record_ids,omitempty"`
	Calibrations     map[string]TokenCalibration `json:"token_calibrations,omitempty"`
	Total            int64                       `json:"total"`
	OK               int64                       `json:"ok"`
	Failed           int64                       `json:"failed"`
	InputTok         int64                       `json:"input_tokens"`
	OutputTok        int64                       `json:"output_tokens"`
	ReasonTok        int64                       `json:"reasoning_tokens"`
	DurSum           int64                       `json:"duration_sum"`
	GenMsSum         int64                       `json:"gen_ms_sum,omitempty"`
	GenTokSum        int64                       `json:"gen_tok_sum,omitempty"`
	GenCount         int64                       `json:"gen_count,omitempty"`
	ByModel          map[string]persistCounter   `json:"by_model"`
	ByProto          map[string]persistCounter   `json:"by_proto"`
	ByAccount        map[string]persistCounter   `json:"by_account,omitempty"`
	Recent           []Record                    `json:"recent"`
	Series           []minuteBucket              `json:"series"`
	SavedAt          int64                       `json:"saved_at"`
	UsageHistory     *UsageHistory               `json:"usage_history,omitempty"`
}

// Save 把当前指标写入 path；path 为空时直接跳过。
//
// 写盘采用「临时文件 + rename」的原子方式，避免进程在写一半时被信号打断而
// 留下半截文件，导致下次启动 Load 失败。
func (m *Metrics) Save(path string) error {
	if path == "" {
		return nil
	}
	m.mu.Lock()
	history := m.historySnapshot()
	p := persisted{
		Total:        m.total.Load(),
		OK:           m.okCount.Load(),
		Failed:       m.failed.Load(),
		InputTok:     m.inputTok.Load(),
		OutputTok:    m.outputTok.Load(),
		ReasonTok:    m.reasonTok.Load(),
		DurSum:       m.durSum.Load(),
		GenMsSum:     m.genMsSum.Load(),
		GenTokSum:    m.genTokSum.Load(),
		GenCount:     m.genCount.Load(),
		ByModel:      cloneCounters(m.byModel),
		ByProto:      cloneCounters(m.byProto),
		ByAccount:    cloneCounters(m.byAccount),
		Recent:       append([]Record(nil), m.recent...),
		Series:       append([]minuteBucket(nil), m.series...),
		SavedAt:      time.Now().Unix(),
		UsageHistory: &history,
		Calibrations: make(map[string]TokenCalibration, len(m.calibrations)),
	}
	for key, value := range m.calibrations {
		p.Calibrations[key] = value
	}
	for key := range m.historyIDs {
		p.HistoryRecordIDs = append(p.HistoryRecordIDs, key)
	}
	m.mu.Unlock()

	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(path, b); err != nil {
		return err
	}
	m.savedAt.Store(p.SavedAt)
	return nil
}

func (m *Metrics) SavedAt() time.Time {
	if stamp := m.savedAt.Load(); stamp > 0 {
		return time.Unix(stamp, 0).UTC()
	}
	return time.Time{}
}

// Load 从 path 重建累积指标；文件不存在时视为首次运行，直接返回。
//
// 仅恢复可累积的计数与历史明细，不覆盖进程本次启动时间（uptime 仍是本进程时长）。
func (m *Metrics) Load(path string) error {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total.Store(p.Total)
	m.okCount.Store(p.OK)
	m.failed.Store(p.Failed)
	m.inputTok.Store(p.InputTok)
	m.outputTok.Store(p.OutputTok)
	m.reasonTok.Store(p.ReasonTok)
	m.durSum.Store(p.DurSum)
	m.genMsSum.Store(p.GenMsSum)
	m.genTokSum.Store(p.GenTokSum)
	m.genCount.Store(p.GenCount)
	m.savedAt.Store(p.SavedAt)
	m.byModel = rebuildCounters(p.ByModel)
	m.byProto = rebuildCounters(p.ByProto)
	m.byAccount = rebuildCounters(p.ByAccount)
	m.recent = append([]Record(nil), p.Recent...)
	m.series = normalizeMinuteBuckets(p.Series)
	if p.UsageHistory != nil {
		p.UsageHistory.RecordIDs = p.HistoryRecordIDs
	}
	m.loadHistory(p.UsageHistory, p.Recent, p.Total)
	m.calibrations = make(map[string]TokenCalibration)
	for key, value := range p.Calibrations {
		m.calibrations[key] = value
	}
	return nil
}

func cloneCounters(src map[string]*groupCounter) map[string]persistCounter {
	out := make(map[string]persistCounter, len(src))
	for k, c := range src {
		out[k] = persistCounter{
			Total: c.Total, OK: c.OK, Failed: c.Failed,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, DurationSum: c.DurationSum,
		}
	}
	return out
}

func rebuildCounters(src map[string]persistCounter) map[string]*groupCounter {
	out := make(map[string]*groupCounter, len(src))
	for k, c := range src {
		out[k] = &groupCounter{
			Total: c.Total, OK: c.OK, Failed: c.Failed,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, DurationSum: c.DurationSum,
		}
	}
	return out
}

func atomicWrite(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
