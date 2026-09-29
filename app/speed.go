package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	mrand "math/rand"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// How a speed test works (this is why it matches speedtest.net much better than one 5 MB fetch):
//   - several TCP connections at the same time (one connection cannot fill a fast or high-latency line),
//   - runs for a fixed time instead of a fixed size,
//   - the first seconds (TCP slow-start, TLS handshakes) are not counted.
const (
	speedDuration = 10 * time.Second
	speedGrace    = 2 * time.Second
	dlStreams     = 6
	ulStreams     = 4
	dlChunk       = 25_000_000
	ulMinChunk    = 256 << 10
	ulMaxChunk    = 64 << 20
	ooklaCacheFor = 30 * time.Minute
)

type speedEndpoint struct {
	name  string
	dlURL func() string
	ulURL func() string
}

func cloudflareEndpoint() speedEndpoint {
	return speedEndpoint{
		name:  "Cloudflare",
		dlURL: func() string { return fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", dlChunk) },
		ulURL: func() string { return "https://speed.cloudflare.com/__up" },
	}
}

type ooklaServer struct {
	Host    string `json:"host"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Sponsor string `json:"sponsor"`
	HTTPS   int    `json:"https_functional"`
	latency time.Duration
}

func (s *ooklaServer) label() string {
	return fmt.Sprintf("%s (%s, %s)", s.Sponsor, s.Name, s.Country)
}

func (s *ooklaServer) endpoint() speedEndpoint {
	return speedEndpoint{
		name: s.label(),
		dlURL: func() string {
			return fmt.Sprintf("https://%s/download?nocache=%d&size=%d", s.Host, mrand.Int63(), dlChunk)
		},
		ulURL: func() string { return fmt.Sprintf("https://%s/upload?nocache=%d", s.Host, mrand.Int63()) },
	}
}

// ===================== QUEUE / SCHEDULE =====================

// EnqueueSpeed queues a test. Tests always run one after another (never at the
// same time), because two parallel tests would share the line and both be wrong.
func (e *Engine) EnqueueSpeed(kind string) bool {
	if kind != "dl" && kind != "ul" {
		return false
	}
	e.mu.Lock()
	if e.speedBusy[kind] {
		e.mu.Unlock()
		return false
	}
	e.speedBusy[kind] = true
	e.publishLocked("speedstate", map[string]bool{"dl": e.speedBusy["dl"], "ul": e.speedBusy["ul"]})
	e.mu.Unlock()
	e.speedQueue <- kind
	return true
}

func (e *Engine) speedWorker() {
	for kind := range e.speedQueue {
		e.runSpeed(kind)
		e.mu.Lock()
		e.speedBusy[kind] = false
		e.publishLocked("speedstate", map[string]bool{"dl": e.speedBusy["dl"], "ul": e.speedBusy["ul"]})
		e.mu.Unlock()
	}
}

// rescheduleLocked restarts the auto-test timer of one kind. Caller holds e.mu.
func (e *Engine) rescheduleLocked(kind string) {
	tp, mins := &e.dlTimer, e.cfg.DLInterval
	if kind == "ul" {
		tp, mins = &e.ulTimer, e.cfg.ULInterval
	}
	if *tp != nil {
		(*tp).Stop()
		*tp = nil
	}
	if mins < 1 {
		return
	}
	*tp = time.AfterFunc(time.Duration(mins)*time.Minute, func() {
		e.EnqueueSpeed(kind)
		e.mu.Lock()
		e.rescheduleLocked(kind)
		e.mu.Unlock()
	})
}

// ===================== RUN =====================

func (e *Engine) runSpeed(kind string) {
	e.mu.Lock()
	srv := e.cfg.SpeedServer
	e.mu.Unlock()

	ep := cloudflareEndpoint()
	if srv == "ookla" {
		s, err := e.pickOokla()
		if err != nil {
			e.logf(true, "Ookla server list failed (%s) — using Cloudflare", html.EscapeString(err.Error()))
		} else {
			ep = s.endpoint()
		}
	}

	name := map[string]string{"dl": "Download", "ul": "Upload"}[kind]
	e.logf(false, "%s speed test started — %s", name, html.EscapeString(ep.name))
	progress := func(mbps float64) {
		e.publish("speedprogress", map[string]any{"type": kind, "mbps": round1(mbps)})
	}

	var mbps float64
	var bytes int64
	var err error
	if kind == "dl" {
		mbps, bytes, err = measureDownload(ep.dlURL, progress)
	} else {
		mbps, bytes, err = measureUpload(ep.ulURL, progress)
	}
	if err != nil || mbps <= 0 {
		if err == nil {
			err = errors.New("no data")
		}
		e.logf(true, "%s test failed: %s", name, html.EscapeString(err.Error()))
		e.mu.Lock()
		e.ookla = nil // pick a server again next time
		e.mu.Unlock()
		e.addSpeedResult(SpeedResult{Time: time.Now().UnixMilli(), Type: kind, Mbps: 0, Server: ep.name})
		return
	}
	mbps = round1(mbps)
	e.logf(false, `%s: <span class="ms">%s Mbps</span> (%.0f MB used)`, name, fmtMbps(mbps), float64(bytes)/1e6)
	e.addSpeedResult(SpeedResult{Time: time.Now().UnixMilli(), Type: kind, Mbps: mbps, Server: ep.name})
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func fmtMbps(x float64) string {
	if x < 10 {
		return fmt.Sprintf("%.1f", x)
	}
	return fmt.Sprintf("%.0f", x)
}

// newSpeedClient uses HTTP/1.1 only: with HTTP/2 all "parallel" requests would
// share one TCP connection, which is exactly what we want to avoid.
func newSpeedClient() (*http.Client, *http.Transport) {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 16,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &http.Client{Transport: tr}, tr
}

// ===================== DOWNLOAD =====================

func measureDownload(urlFn func() string, progress func(float64)) (float64, int64, error) {
	client, tr := newSpeedClient()
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), speedDuration)
	defer cancel()

	var total atomic.Int64
	var lastErr atomic.Value
	var wg sync.WaitGroup
	for i := 0; i < dlStreams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fails := 0
			for ctx.Err() == nil {
				if err := downloadOnce(ctx, client, urlFn(), &total); err != nil && ctx.Err() == nil {
					lastErr.Store(err)
					if fails++; fails >= 3 {
						return
					}
					time.Sleep(300 * time.Millisecond)
				} else {
					fails = 0
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	t0 := time.Now()
	var tGrace time.Time
	var graceBytes int64
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-tick.C:
			now, b := time.Now(), total.Load()
			if tGrace.IsZero() && now.Sub(t0) >= speedGrace {
				tGrace, graceBytes = now, b
			}
			if !tGrace.IsZero() && now.Sub(tGrace) > 200*time.Millisecond {
				progress(float64(b-graceBytes) * 8 / now.Sub(tGrace).Seconds() / 1e6)
			} else {
				progress(float64(b) * 8 / now.Sub(t0).Seconds() / 1e6)
			}
		case <-done:
			break loop
		}
	}
	tEnd, b := time.Now(), total.Load()
	if b == 0 {
		return 0, 0, errOr(&lastErr, "no data received")
	}
	if !tGrace.IsZero() && tEnd.Sub(tGrace) > time.Second {
		return float64(b-graceBytes) * 8 / tEnd.Sub(tGrace).Seconds() / 1e6, b, nil
	}
	return float64(b) * 8 / tEnd.Sub(t0).Seconds() / 1e6, b, nil
}

func downloadOnce(ctx context.Context, c *http.Client, url string, total *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		total.Add(int64(n))
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ===================== UPLOAD =====================

// Upload is measured from *completed* requests only (the server confirmed it
// received them). Counting bytes as we write them would be wrong, because the
// OS send buffer (often several MB) swallows data long before it is sent.
// Each connection grows its request size until one request takes ~0.5 s or more.

type ulReq struct {
	start, end time.Time
	bytes      int64
}

var uploadBuf = func() []byte { b := make([]byte, 64<<10); rand.Read(b); return b }()

type genReader struct {
	left    int64
	counter *atomic.Int64
}

func (g *genReader) Read(p []byte) (int, error) {
	if g.left <= 0 {
		return 0, io.EOF
	}
	n := copy(p, uploadBuf)
	if int64(n) > g.left {
		n = int(g.left)
	}
	g.left -= int64(n)
	g.counter.Add(int64(n))
	return n, nil
}

func measureUpload(urlFn func() string, progress func(float64)) (float64, int64, error) {
	client, tr := newSpeedClient()
	defer tr.CloseIdleConnections()
	t0 := time.Now()
	deadline := t0.Add(speedDuration)
	// In-flight requests may finish up to 3 s after the deadline; later ones are dropped.
	ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(3*time.Second))
	defer cancel()

	var written atomic.Int64 // only for the live progress number
	var lastErr atomic.Value
	perStream := make([][]ulReq, ulStreams)
	var wg sync.WaitGroup
	for i := 0; i < ulStreams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chunk, fails := int64(ulMinChunk), 0
			for time.Now().Before(deadline) && ctx.Err() == nil {
				s := time.Now()
				err := uploadOnce(ctx, client, urlFn(), chunk, &written)
				e := time.Now()
				if err != nil {
					if ctx.Err() == nil {
						lastErr.Store(err)
					}
					if fails++; fails >= 3 {
						return
					}
					time.Sleep(300 * time.Millisecond)
					continue
				}
				fails = 0
				perStream[i] = append(perStream[i], ulReq{s, e, chunk})
				if e.Sub(s) < 500*time.Millisecond && chunk < ulMaxChunk {
					chunk *= 2
				}
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-tick.C:
			if el := time.Since(t0).Seconds(); el > 0 && time.Now().Before(deadline) {
				progress(float64(written.Load()) * 8 / el / 1e6)
			}
		case <-done:
			break loop
		}
	}

	ws := t0.Add(speedGrace)
	var mbps float64
	var confirmed int64
	for _, reqs := range perStream {
		if len(reqs) == 0 {
			continue
		}
		var sum int64
		for _, r := range reqs {
			sum += r.bytes
		}
		confirmed += sum
		we := reqs[len(reqs)-1].end
		if we.Sub(ws) > 500*time.Millisecond {
			// Bytes that moved inside [ws, we], each request spread evenly over its own duration.
			var b float64
			for _, r := range reqs {
				ov := minTime(r.end, we).Sub(maxTime(r.start, ws))
				if ov > 0 {
					b += float64(r.bytes) * ov.Seconds() / r.end.Sub(r.start).Seconds()
				}
			}
			mbps += b * 8 / we.Sub(ws).Seconds() / 1e6
		} else {
			// Very slow line: nothing finished after the warm-up, use everything.
			mbps += float64(sum) * 8 / we.Sub(reqs[0].start).Seconds() / 1e6
		}
	}
	if confirmed == 0 {
		return 0, 0, errOr(&lastErr, "no upload confirmed")
	}
	return mbps, confirmed, nil
}

func uploadOnce(ctx context.Context, c *http.Client, url string, size int64, written *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, "POST", url, &genReader{left: size, counter: written})
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ===================== OOKLA SERVER PICK =====================

// pickOokla asks speedtest.net for servers near this IP and picks the one with
// the lowest latency (the same idea as speedtest.net's automatic choice).
func (e *Engine) pickOokla() (*ooklaServer, error) {
	e.mu.Lock()
	if e.ookla != nil && time.Since(e.ooklaAt) < ooklaCacheFor {
		s := e.ookla
		e.mu.Unlock()
		return s, nil
	}
	e.mu.Unlock()

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://www.speedtest.net/api/js/servers?engine=js&https_functional=true&limit=10", nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list []*ooklaServer
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("bad server list: %v", err)
	}
	var ok []*ooklaServer
	for _, s := range list {
		if s.Host != "" && s.HTTPS == 1 {
			ok = append(ok, s)
		}
	}
	if len(ok) == 0 {
		return nil, errors.New("no servers")
	}

	var wg sync.WaitGroup
	for _, s := range ok {
		wg.Add(1)
		go func(s *ooklaServer) {
			defer wg.Done()
			s.latency = ooklaLatency(s.Host)
		}(s)
	}
	wg.Wait()
	sort.Slice(ok, func(i, j int) bool { return ok[i].latency < ok[j].latency })
	best := ok[0]
	if best.latency == math.MaxInt64 {
		return nil, errors.New("no server answered")
	}
	e.logf(false, "Ookla server: %s — %d ms", html.EscapeString(best.label()), best.latency.Milliseconds())
	e.mu.Lock()
	e.ookla, e.ooklaAt = best, time.Now()
	e.mu.Unlock()
	return best, nil
}

// ooklaLatency returns the best of 3 requests on one kept-alive connection
// (so the TCP/TLS handshake is not counted), or MaxInt64 on failure.
func ooklaLatency(host string) time.Duration {
	client, tr := newSpeedClient()
	client.Timeout = 3 * time.Second
	defer tr.CloseIdleConnections()
	best := time.Duration(math.MaxInt64)
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		resp, err := client.Get(fmt.Sprintf("https://%s/hello?nocache=%d", host, mrand.Int63()))
		if err != nil {
			return best
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if d := time.Since(t0); resp.StatusCode == 200 && d < best {
			best = d
		}
	}
	return best
}

func errOr(v *atomic.Value, def string) error {
	if e, ok := v.Load().(error); ok {
		return e
	}
	return errors.New(def)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
