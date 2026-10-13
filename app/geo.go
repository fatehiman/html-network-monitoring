package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ===================== IP GEO (country, city, ISP of the exit IP) =====================
//
// Used by the Windows tray tooltip. The endpoint answers an HTML page with
//   Country: <span …>X</span><br>City: <span …>Y</span><br>ISP: <span …>Z</span>
// Results are cached in <data>/geoip.json (max geoCacheMax IPs, geoCacheTTL each).

const (
	geoURL      = "https://ip.peppasoft.com/?ip="
	geoCacheTTL = 10 * 24 * time.Hour
	geoCacheMax = 50
)

var geoFieldRe = map[string]*regexp.Regexp{}

func init() {
	for _, f := range []string{"Country", "City", "ISP"} {
		geoFieldRe[f] = regexp.MustCompile(`(?:^|>)` + f + `: <span[^>]*>([^<]*)</span>`)
	}
}

// parseGeo returns "Country - City - ISP" (empty parts are skipped).
// It fails if a field is missing (unknown format) or all fields are empty (unknown IP).
func parseGeo(body string) (string, error) {
	var parts []string
	for _, f := range []string{"Country", "City", "ISP"} {
		m := geoFieldRe[f].FindStringSubmatch(body)
		if m == nil {
			return "", fmt.Errorf("unexpected response: no %s field", f)
		}
		if v := strings.Join(strings.Fields(html.UnescapeString(m[1])), " "); v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no location data")
	}
	return strings.Join(parts, " - "), nil
}

type geoEntry struct {
	Text string `json:"text"`
	At   int64  `json:"at"` // ms
}

type GeoLookup struct {
	path   string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]geoEntry
}

func newGeoLookup(dataDir string) *GeoLookup {
	g := &GeoLookup{
		path:   filepath.Join(dataDir, "geoip.json"),
		client: &http.Client{Timeout: 8 * time.Second},
		cache:  map[string]geoEntry{},
	}
	if b, err := os.ReadFile(g.path); err == nil {
		json.Unmarshal(b, &g.cache)
	}
	return g
}

// Lookup returns the location text of ip, from the cache or from the endpoint.
func (g *GeoLookup) Lookup(ip string) (string, error) {
	now := time.Now()
	g.mu.Lock()
	if c, ok := g.cache[ip]; ok && now.Sub(time.UnixMilli(c.At)) < geoCacheTTL {
		g.mu.Unlock()
		return c.Text, nil
	}
	g.mu.Unlock()

	text, err := g.fetch(ip)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cache[ip] = geoEntry{Text: text, At: now.UnixMilli()}
	g.pruneLocked(now)
	if b, err := json.Marshal(g.cache); err == nil {
		writeFileAtomic(g.path, b)
	}
	return text, nil
}

func (g *GeoLookup) fetch(ip string) (string, error) {
	req, err := http.NewRequest("GET", geoURL+url.QueryEscape(ip), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	return parseGeo(string(b))
}

// pruneLocked drops expired entries, then the oldest ones above geoCacheMax.
func (g *GeoLookup) pruneLocked(now time.Time) {
	for ip, c := range g.cache {
		if now.Sub(time.UnixMilli(c.At)) >= geoCacheTTL {
			delete(g.cache, ip)
		}
	}
	for len(g.cache) > geoCacheMax {
		oldest, at := "", int64(0)
		for ip, c := range g.cache {
			if oldest == "" || c.At < at {
				oldest, at = ip, c.At
			}
		}
		delete(g.cache, oldest)
	}
}
