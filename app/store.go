package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	rangeDur   = 2 * time.Hour
	maxAgeDays = 10
	maxRanges  = 120
)

// Point is one probe result. MS == nil means failure/timeout.
// JSON form is [timestampMs, ms|null] (same as the HTML version's localStorage).
type Point struct {
	T  int64
	MS *int
}

func (p Point) MarshalJSON() ([]byte, error) {
	if p.MS == nil {
		return []byte(fmt.Sprintf("[%d,null]", p.T)), nil
	}
	return []byte(fmt.Sprintf("[%d,%d]", p.T, *p.MS)), nil
}

func (p *Point) UnmarshalJSON(b []byte) error {
	var a []*int64
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 2 || a[0] == nil {
		return fmt.Errorf("bad point")
	}
	p.T = *a[0]
	if a[1] != nil {
		v := int(*a[1])
		p.MS = &v
	}
	return nil
}

type Tag struct {
	Text  string `json:"text"`
	Color string `json:"color"`
	Start int64  `json:"start"`
	End   *int64 `json:"end"`
}

type SpeedResult struct {
	Time   int64   `json:"time"`
	Type   string  `json:"type"` // "dl" | "ul"
	Mbps   float64 `json:"mbps"` // 0 = failed
	Server string  `json:"server,omitempty"`
}

type RangeData struct {
	Points []Point       `json:"points"`
	Tags   []Tag         `json:"tags"`
	Speed  []SpeedResult `json:"speed"`
}

func newRangeData() *RangeData {
	return &RangeData{Points: []Point{}, Tags: []Tag{}, Speed: []SpeedResult{}}
}

// ---- range keys ----

func rangeStart(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour()/2*2, 0, 0, 0, time.Local)
}

// rangeKey returns e.g. "2026/03/06 00:00-02:00" (same format as the HTML version).
func rangeKey(start time.Time) string {
	end := start.Add(rangeDur)
	return start.Format("2006/01/02 15:04") + "-" + end.Format("15:04")
}

func parseRangeKey(k string) (time.Time, bool) {
	if len(k) < 16 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006/01/02 15:04", k[:16], time.Local)
	return t, err == nil
}

// ---- files ----

type Store struct {
	dir string // <data>/ranges
}

func newStore(dataDir string) (*Store, error) {
	d := filepath.Join(dataDir, "ranges")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: d}, nil
}

func (s *Store) file(start time.Time) string {
	return filepath.Join(s.dir, start.Format("20060102-15")+".json")
}

func (s *Store) Load(start time.Time) *RangeData {
	rd := newRangeData()
	b, err := os.ReadFile(s.file(start))
	if err != nil {
		return rd
	}
	if json.Unmarshal(b, rd) != nil {
		return newRangeData()
	}
	if rd.Points == nil {
		rd.Points = []Point{}
	}
	if rd.Tags == nil {
		rd.Tags = []Tag{}
	}
	if rd.Speed == nil {
		rd.Speed = []SpeedResult{}
	}
	return rd
}

func (s *Store) Save(start time.Time, rd *RangeData) error {
	b, err := json.Marshal(rd)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.file(start), b)
}

// Starts returns the start time of every stored range, oldest first.
func (s *Store) Starts() []time.Time {
	ents, _ := os.ReadDir(s.dir)
	var out []time.Time
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		t, err := time.ParseInLocation("20060102-15", strings.TrimSuffix(n, ".json"), time.Local)
		if err == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func (s *Store) Delete(start time.Time) { os.Remove(s.file(start)) }

func (s *Store) Purge() {
	cut := time.Now().Add(-maxAgeDays * 24 * time.Hour)
	var keep []time.Time
	for _, t := range s.Starts() {
		if t.Before(cut) {
			s.Delete(t)
		} else {
			keep = append(keep, t)
		}
	}
	for len(keep) > maxRanges {
		s.Delete(keep[0])
		keep = keep[1:]
	}
}

func (s *Store) DeleteAll() {
	for _, t := range s.Starts() {
		s.Delete(t)
	}
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
