package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestAddUserURLKeepsLastFive(t *testing.T) {
	var l []string
	for _, u := range []string{"a", "b", "c", "d", "e", "f"} {
		l = addUserURL(l, u)
	}
	if want := []string{"b", "c", "d", "e", "f"}; !reflect.DeepEqual(l, want) {
		t.Fatalf("got %v want %v", l, want)
	}
	// Adding an existing URL moves it to the end instead of duplicating it.
	l = addUserURL(l, "c")
	if want := []string{"b", "d", "e", "f", "c"}; !reflect.DeepEqual(l, want) {
		t.Fatalf("got %v want %v", l, want)
	}
}

func TestPointJSON(t *testing.T) {
	ms := 42
	b, _ := json.Marshal([]Point{{T: 1, MS: &ms}, {T: 2}})
	if string(b) != "[[1,42],[2,null]]" {
		t.Fatalf("got %s", b)
	}
	var back []Point
	if err := json.Unmarshal(b, &back); err != nil || *back[0].MS != 42 || back[1].MS != nil {
		t.Fatalf("round trip failed: %v %v", back, err)
	}
}

func TestRangeKey(t *testing.T) {
	s := time.Date(2026, 3, 6, 22, 0, 0, 0, time.Local)
	if k := rangeKey(s); k != "2026/03/06 22:00-00:00" {
		t.Fatalf("got %q", k)
	}
	if p, ok := parseRangeKey(rangeKey(s)); !ok || !p.Equal(s) {
		t.Fatalf("parse failed")
	}
	if r := rangeStart(time.Date(2026, 3, 6, 23, 59, 0, 0, time.Local)); !r.Equal(s) {
		t.Fatalf("rangeStart got %v", r)
	}
}

func TestRolloverContinuesOpenTag(t *testing.T) {
	e, err := newEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := rangeStart(time.Now()).Add(-rangeDur)
	e.liveStart = prev
	e.live = newRangeData()
	e.live.Tags = []Tag{{Text: "4G", Color: "x", Start: prev.UnixMilli()}}

	e.record(time.Now(), nil, "u")

	old := e.store.Load(prev)
	if old.Tags[0].End == nil || *old.Tags[0].End != prev.Add(rangeDur).UnixMilli() {
		t.Fatalf("old tag not closed at boundary: %+v", old.Tags[0])
	}
	if len(e.live.Tags) != 1 || e.live.Tags[0].Text != "4G" || e.live.Tags[0].End != nil ||
		e.live.Tags[0].Start != rangeStart(time.Now()).UnixMilli() {
		t.Fatalf("tag not continued: %+v", e.live.Tags)
	}
	if len(e.live.Points) != 1 {
		t.Fatalf("point not stored in new range")
	}
}

func TestTagColorMatchesJS(t *testing.T) {
	// Expected values computed with tagColor() from ping9.htm in Node.
	for text, want := range map[string]string{
		"WiFi-Home": "hsla(151,65%,50%,0.5)",
		"4G":        "hsla(243,65%,50%,0.5)",
		"وای‌فای":   "hsla(15,65%,50%,0.5)",
		"x😀":        "hsla(19,65%,50%,0.5)",
	} {
		if c := tagColor(text); c != want {
			t.Errorf("%q: got %s want %s", text, c, want)
		}
	}
}
