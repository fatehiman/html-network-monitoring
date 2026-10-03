package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	probeTimeout = 5 * time.Second
	maxLogs      = 200
)

type LogEntry struct {
	T   int64  `json:"t"`
	Msg string `json:"msg"` // may contain HTML spans; user text is escaped
	Err bool   `json:"err,omitempty"`
}

type Engine struct {
	dataDir string
	store   *Store

	mu        sync.Mutex
	cfg       Config
	running   bool
	liveStart time.Time
	live      *RangeData
	logs      []LogEntry
	subs      map[chan []byte]struct{}
	wake      chan struct{}

	// speed tests
	speedBusy  map[string]bool // "dl"/"ul" -> queued or running
	speedQueue chan string
	dlTimer    *time.Timer
	ulTimer    *time.Timer
	ookla      *ooklaServer // cached nearest server
	ooklaAt    time.Time

	probeClient *http.Client

	// exit IP, refreshed on a timer and optionally auto-tagged
	exitIP        string // last successfully fetched IP
	exitIPOK      bool   // false if the most recent fetch failed
	exitIPErr     string // error of the most recent fetch, if it failed
	exitIPAt      int64  // time of the most recent fetch attempt
	autoTagLastIP string
	ipifyClient   *http.Client
	ipifyWake     chan struct{} // wakes ipifyLoop early (endpoint changed)
}

func newEngine(dataDir string) (*Engine, error) {
	st, err := newStore(dataDir)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		dataDir:    dataDir,
		store:      st,
		cfg:        loadConfig(dataDir),
		running:    true,
		subs:       map[chan []byte]struct{}{},
		wake:       make(chan struct{}, 1),
		speedBusy:  map[string]bool{},
		speedQueue: make(chan string, 4),
		probeClient: &http.Client{
			Timeout: probeTimeout,
			// Do not follow redirects: the first response is the round-trip we want.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DisableCompression:  true,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: probeTimeout,
			},
		},
		ipifyClient: &http.Client{Timeout: 8 * time.Second},
		ipifyWake:   make(chan struct{}, 1),
	}
	st.Purge()
	e.liveStart = rangeStart(time.Now())
	e.live = st.Load(e.liveStart)
	return e, nil
}

func (e *Engine) Start() {
	e.logf(false, "Monitor started — target: %s", html.EscapeString(e.cfg.URL))
	go e.monitorLoop()
	go e.speedWorker()
	go e.ipifyLoop()
	e.mu.Lock()
	e.rescheduleLocked("dl")
	e.rescheduleLocked("ul")
	e.mu.Unlock()
}

// ===================== EVENTS =====================

func (e *Engine) Subscribe() chan []byte {
	ch := make(chan []byte, 256)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch
}

func (e *Engine) Unsubscribe(ch chan []byte) {
	e.mu.Lock()
	delete(e.subs, ch)
	e.mu.Unlock()
}

// publishLocked sends an event to every UI. Caller holds e.mu.
func (e *Engine) publishLocked(typ string, data any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	for ch := range e.subs {
		select {
		case ch <- b:
		default: // slow client: drop
		}
	}
}

func (e *Engine) publish(typ string, data any) {
	e.mu.Lock()
	e.publishLocked(typ, data)
	e.mu.Unlock()
}

func (e *Engine) logf(isErr bool, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	le := LogEntry{T: time.Now().UnixMilli(), Msg: msg, Err: isErr}
	e.mu.Lock()
	e.logs = append(e.logs, le)
	if len(e.logs) > maxLogs {
		e.logs = e.logs[len(e.logs)-maxLogs:]
	}
	e.publishLocked("log", le)
	e.mu.Unlock()
	if !quiet {
		log.Println(stripTags(msg))
	}
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(b.String())
}

// ===================== STATE =====================

type State struct {
	Config    Config          `json:"config"`
	Presets   []Preset        `json:"presets"`
	Running   bool            `json:"running"`
	LiveKey   string          `json:"liveKey"`
	Ranges    []string        `json:"ranges"` // oldest first
	Logs      []LogEntry      `json:"logs"`
	SpeedBusy map[string]bool `json:"speedBusy"`
	Version   string          `json:"version"`
	// NativeSound: the program plays the beeps itself (Windows tray), so the page must not.
	NativeSound bool   `json:"nativeSound"`
	ExitIP      string `json:"exitIp"`
	ExitIPOK    bool   `json:"exitIpOk"`
	ExitIPErr   string `json:"exitIpErr"`
	ExitIPAt    int64  `json:"exitIpAt"`
}

func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return State{
		Config:    e.cfg,
		Presets:   presets,
		Running:   e.running,
		LiveKey:   rangeKey(e.liveStart),
		Ranges:    e.rangeKeysLocked(),
		Logs:      append([]LogEntry{}, e.logs...),
		SpeedBusy: map[string]bool{"dl": e.speedBusy["dl"], "ul": e.speedBusy["ul"]},
		Version:   version,

		NativeSound: nativeSound,
		ExitIP:      e.exitIP,
		ExitIPOK:    e.exitIPOK,
		ExitIPErr:   e.exitIPErr,
		ExitIPAt:    e.exitIPAt,
	}
}

func (e *Engine) rangeKeysLocked() []string {
	now := rangeStart(time.Now())
	var keys []string
	hasLive := false
	for _, t := range e.store.Starts() {
		if t.After(now) {
			continue
		}
		if t.Equal(e.liveStart) {
			hasLive = true
		}
		keys = append(keys, rangeKey(t))
	}
	if !hasLive {
		keys = append(keys, rangeKey(e.liveStart))
	}
	return keys
}

func (e *Engine) Range(key string) (*RangeData, bool) {
	t, ok := parseRangeKey(key)
	if !ok {
		return nil, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if t.Equal(e.liveStart) {
		b, _ := json.Marshal(e.live) // copy under lock
		rd := newRangeData()
		json.Unmarshal(b, rd)
		return rd, true
	}
	return e.store.Load(t), true
}

// ===================== CONFIG =====================

type ConfigPatch struct {
	URL         *string `json:"url"`
	Interval    *int    `json:"interval"`
	DLInterval  *int    `json:"dlInterval"`
	ULInterval  *int    `json:"ulInterval"`
	SpeedServer *string `json:"speedServer"`
	SoundMode   *string `json:"soundMode"`
	AutoTagIP   *bool   `json:"autoTagIp"`
	IPURL       *string `json:"ipUrl"`
}

func (e *Engine) UpdateConfig(p ConfigPatch) Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	oldDL, oldUL := e.cfg.DLInterval, e.cfg.ULInterval
	if p.URL != nil {
		e.cfg.URL = strings.TrimSpace(*p.URL)
	}
	if p.Interval != nil {
		e.cfg.Interval = *p.Interval
	}
	if p.DLInterval != nil {
		e.cfg.DLInterval = *p.DLInterval
	}
	if p.ULInterval != nil {
		e.cfg.ULInterval = *p.ULInterval
	}
	if p.SpeedServer != nil {
		e.cfg.SpeedServer = *p.SpeedServer
	}
	if p.SoundMode != nil {
		e.cfg.SoundMode = *p.SoundMode
	}
	if p.AutoTagIP != nil {
		e.cfg.AutoTagIP = *p.AutoTagIP
	}
	if p.IPURL != nil && *p.IPURL != e.cfg.IPURL {
		e.cfg.IPURL = *p.IPURL
		e.wakeIpifyLocked()
	}
	e.cfg.normalize()
	if e.cfg.DLInterval != oldDL {
		e.rescheduleLocked("dl")
	}
	if e.cfg.ULInterval != oldUL {
		e.rescheduleLocked("ul")
	}
	e.saveConfigLocked()
	return e.cfg
}

func (e *Engine) saveConfigLocked() {
	if err := saveConfig(e.dataDir, e.cfg); err != nil {
		log.Println("save config:", err)
	}
	e.publishLocked("config", e.cfg)
}

func (e *Engine) AddUserURL(u string) Config {
	u = strings.TrimSpace(u)
	e.mu.Lock()
	defer e.mu.Unlock()
	if u != "" {
		e.cfg.URL = u // adding a URL also makes it the target
		if !isPreset(u) {
			e.cfg.UserURLs = addUserURL(e.cfg.UserURLs, u)
		}
		e.saveConfigLocked()
	}
	return e.cfg
}

func (e *Engine) RemoveUserURL(u string) Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []string{}
	for _, x := range e.cfg.UserURLs {
		if x != u {
			out = append(out, x)
		}
	}
	e.cfg.UserURLs = out
	e.saveConfigLocked()
	return e.cfg
}

func (e *Engine) AddIPURL(u string) Config {
	u = strings.TrimSpace(u)
	e.mu.Lock()
	defer e.mu.Unlock()
	if u != "" && !isIPPreset(u) {
		e.cfg.IPURLList = addIPURL(e.cfg.IPURLList, u)
		e.saveConfigLocked()
	}
	return e.cfg
}

func (e *Engine) RemoveIPURL(u string) Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []string{}
	for _, x := range e.cfg.IPURLList {
		if x != u {
			out = append(out, x)
		}
	}
	e.cfg.IPURLList = out
	if e.cfg.IPURL == u {
		e.cfg.IPURL = ipPresets[0]
		e.wakeIpifyLocked()
	}
	e.saveConfigLocked()
	return e.cfg
}

// wakeIpifyLocked makes ipifyLoop fetch again right away instead of waiting out the interval.
// Caller holds e.mu.
func (e *Engine) wakeIpifyLocked() {
	select {
	case e.ipifyWake <- struct{}{}:
	default:
	}
}

// ===================== CONTROLS =====================

func (e *Engine) Toggle() bool {
	e.mu.Lock()
	e.running = !e.running
	r := e.running
	e.publishLocked("running", r)
	e.mu.Unlock()
	if r {
		e.logf(false, "Monitor resumed")
		select {
		case e.wake <- struct{}{}:
		default:
		}
	} else {
		e.logf(false, "Monitor paused")
	}
	return r
}

func (e *Engine) ClearAll() {
	e.mu.Lock()
	e.store.DeleteAll()
	e.live = newRangeData()
	e.logs = nil
	e.publishLocked("cleared", rangeKey(e.liveStart))
	e.mu.Unlock()
	e.logf(false, "All data cleared")
}

func (e *Engine) AddTag(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty tag")
	}
	now := time.Now().UnixMilli()
	e.mu.Lock()
	tags := e.live.Tags
	if n := len(tags); n > 0 && tags[n-1].End == nil {
		tags[n-1].End = &now
	}
	e.live.Tags = append(tags, Tag{Text: text, Color: tagColor(text), Start: now})
	e.saveLiveLocked()
	e.publishLocked("tags", map[string]any{"key": rangeKey(e.liveStart), "tags": e.live.Tags})
	e.mu.Unlock()
	e.logf(false, "Tag added: %s", html.EscapeString(text))
	return nil
}

// maybeAutoTag adds a tag for the current exit IP if auto-tagging is on and
// the IP is new (same behavior as the user typing the IP in and pressing Enter).
func (e *Engine) maybeAutoTag() {
	e.mu.Lock()
	on := e.cfg.AutoTagIP
	ip := e.exitIP
	last := e.autoTagLastIP
	e.mu.Unlock()
	if !on || ip == "" || ip == last {
		return
	}
	e.mu.Lock()
	e.autoTagLastIP = ip
	e.mu.Unlock()
	e.AddTag(ip)
}

func (e *Engine) RemoveTag(key string, idx int) {
	t, ok := parseRangeKey(key)
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var rd *RangeData
	if t.Equal(e.liveStart) {
		rd = e.live
	} else {
		rd = e.store.Load(t)
	}
	if idx < 0 || idx >= len(rd.Tags) {
		return
	}
	rd.Tags = append(rd.Tags[:idx], rd.Tags[idx+1:]...)
	e.store.Save(t, rd)
	e.publishLocked("tags", map[string]any{"key": key, "tags": rd.Tags})
}

// tagColor gives the same color as the HTML version for the same text.
func tagColor(text string) string {
	var h int32
	for _, c := range utf16Units(text) {
		h = (h << 5) - h + int32(c)
	}
	hue := ((int(h) % 360) + 360) % 360
	if hue >= 20 && hue <= 40 {
		hue += 40
	}
	return fmt.Sprintf("hsla(%d,65%%,50%%,0.5)", hue)
}

func utf16Units(s string) []uint16 {
	var out []uint16
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}

// ===================== MONITOR LOOP =====================

func (e *Engine) monitorLoop() {
	for {
		e.mu.Lock()
		running := e.running
		url := e.cfg.URL
		interval := time.Duration(e.cfg.Interval) * time.Second
		e.mu.Unlock()

		if !running {
			<-e.wake
			continue
		}

		e.publish("probe", nil) // lets the UI play the "request sent" beep
		ms, finalURL := e.probe(url)
		e.record(time.Now(), ms, finalURL)

		// Wait `interval` after the response (not after the start), like the HTML version.
		select {
		case <-time.After(interval):
		case <-e.wake:
		}
	}
}

// ===================== EXIT IP =====================

const ipifyInterval = 10 * time.Minute

func (e *Engine) ipifyLoop() {
	for {
		e.fetchExitIP()
		select {
		case <-time.After(ipifyInterval):
		case <-e.ipifyWake:
		}
	}
}

func (e *Engine) fetchExitIP() {
	e.mu.Lock()
	url := e.cfg.IPURL
	e.mu.Unlock()

	ip, err := fetchIP(e.ipifyClient, url)
	now := time.Now().UnixMilli()
	e.mu.Lock()
	e.exitIPAt = now
	if err != nil {
		e.exitIPOK = false
		e.exitIPErr = err.Error()
		e.publishLocked("ip", map[string]any{"ok": false, "err": e.exitIPErr, "at": now})
		e.mu.Unlock()
		e.logf(true, "Exit IP lookup failed (%s): %v", url, err)
		return
	}
	e.exitIP = ip
	e.exitIPOK = true
	e.exitIPErr = ""
	e.publishLocked("ip", map[string]any{"ok": true, "ip": ip, "at": now})
	e.mu.Unlock()
	e.maybeAutoTag()
}

// fetchIP does one GET against an exit-IP endpoint and returns the IP, validating
// that the response is actually an IP address (some endpoints answer HTML on error).
func fetchIP(client *http.Client, url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(b))
	if net.ParseIP(ip) == nil {
		if len(ip) > 60 {
			ip = ip[:60] + "…"
		}
		return "", fmt.Errorf("unexpected response: %q", ip)
	}
	return ip, nil
}

func (e *Engine) probe(url string) (*int, string) {
	final := strings.ReplaceAll(url, "{rnd}", fmt.Sprint(10000000+rand.Intn(90000000)))
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", final, nil)
	if err != nil {
		return nil, final
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cache-Control", "no-cache")
	t0 := time.Now()
	resp, err := e.probeClient.Do(req)
	if err != nil {
		return nil, final
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	resp.Body.Close()
	ms := int(time.Since(t0).Milliseconds())
	return &ms, final
}

func (e *Engine) record(now time.Time, ms *int, url string) {
	e.mu.Lock()
	e.rolloverLocked(now)
	p := Point{T: now.UnixMilli(), MS: ms}
	e.live.Points = append(e.live.Points, p)
	e.saveLiveLocked()
	e.publishLocked("point", map[string]any{"key": rangeKey(e.liveStart), "p": p})
	e.mu.Unlock()

	u := html.EscapeString(url)
	if ms != nil {
		e.logf(false, `GET %s → <span class="ms">%d ms</span>`, u, *ms)
	} else {
		e.logf(true, "GET %s → TIMEOUT / ERROR", u)
	}
}

// rolloverLocked starts a new 2-hour range when the clock passes a boundary.
// An open tag is closed at the boundary and continued in the new range.
func (e *Engine) rolloverLocked(now time.Time) {
	rs := rangeStart(now)
	if rs.Equal(e.liveStart) {
		return
	}
	prevKey := rangeKey(e.liveStart)
	next := e.store.Load(rs)
	if n := len(e.live.Tags); n > 0 && e.live.Tags[n-1].End == nil {
		end := e.liveStart.Add(rangeDur).UnixMilli()
		cont := e.live.Tags[n-1]
		e.live.Tags[n-1].End = &end
		next.Tags = append(next.Tags, Tag{Text: cont.Text, Color: cont.Color, Start: rs.UnixMilli()})
	}
	e.saveLiveLocked()
	e.liveStart, e.live = rs, next
	e.saveLiveLocked()
	e.store.Purge()
	e.publishLocked("range", map[string]any{"prev": prevKey, "live": rangeKey(rs), "ranges": e.rangeKeysLocked()})
}

func (e *Engine) saveLiveLocked() {
	if err := e.store.Save(e.liveStart, e.live); err != nil {
		log.Println("save range:", err)
	}
}

func (e *Engine) addSpeedResult(r SpeedResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rolloverLocked(time.Now())
	// Two results of the same type within 60 s: the newer one replaces the older.
	out := e.live.Speed[:0]
	for _, s := range e.live.Speed {
		if !(s.Type == r.Type && r.Time-s.Time < 60000) {
			out = append(out, s)
		}
	}
	e.live.Speed = append(out, r)
	e.saveLiveLocked()
	e.publishLocked("speed", map[string]any{"key": rangeKey(e.liveStart), "speed": e.live.Speed})
}
