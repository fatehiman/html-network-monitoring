package main

import (
	"fmt"
	"testing"
	"time"
)

func TestParseGeo(t *testing.T) {
	sp := `<span style="color:#e33a3a;font-weight:normal">`
	page := func(c, ci, isp string) string {
		return `<pre>IP4: ` + sp + `[ 1.2.3.4 ]</span><br>Country: ` + sp + c + `</span><br>City: ` + sp + ci +
			`</span><br>ISP: ` + sp + isp + `</span><br><br>IP location time info:<br>`
	}
	if got, err := parseGeo(page("Germany", "Frankfurt", "Clouvider Limited")); err != nil || got != "Germany - Frankfurt - Clouvider Limited" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := parseGeo(page("Iran", "", "Aria &amp; Co")); err != nil || got != "Iran - Aria & Co" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseGeo(page("", "", "")); err == nil {
		t.Fatal("empty fields must fail")
	}
	if _, err := parseGeo("<html>502 Bad Gateway</html>"); err == nil {
		t.Fatal("unknown format must fail")
	}
}

func TestGeoCachePrune(t *testing.T) {
	g := newGeoLookup(t.TempDir())
	now := time.Now()
	for i := 0; i < geoCacheMax+5; i++ {
		g.cache[fmt.Sprint(i)] = geoEntry{Text: "x", At: now.Add(time.Duration(i) * time.Second).UnixMilli()}
	}
	g.cache["old"] = geoEntry{Text: "x", At: now.Add(-geoCacheTTL - time.Hour).UnixMilli()}
	g.pruneLocked(now.Add(time.Minute))
	if len(g.cache) != geoCacheMax {
		t.Fatalf("len %d", len(g.cache))
	}
	for _, k := range []string{"old", "0", "4"} {
		if _, ok := g.cache[k]; ok {
			t.Fatalf("%s should be removed", k)
		}
	}
}
