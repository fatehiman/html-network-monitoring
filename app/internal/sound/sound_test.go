package sound

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestWAVFadesInAndOut(t *testing.T) {
	w := WAV(300, 50)
	if string(w[:4]) != "RIFF" || string(w[36:40]) != "data" {
		t.Fatal("bad WAV header")
	}
	n := int(binary.LittleEndian.Uint32(w[40:44])) / 2
	s := make([]int16, n)
	binary.Read(bytes.NewReader(w[44:]), binary.LittleEndian, s)
	// No jump at the edges: the first and last tone samples are (almost) zero.
	lead := SampleRate * leadMs / 1000
	tone := SampleRate * 50 / 1000
	if abs(s[lead]) > 5 || abs(s[lead+tone-1]) > 200 || s[0] != 0 || s[n-1] != 0 {
		t.Fatalf("edges not silent: %d %d", s[lead], s[lead+tone-1])
	}
	var peak int16
	for _, v := range s {
		if abs(v) > peak {
			peak = abs(v)
		}
	}
	if want := Volume * 32767.0; math.Abs(float64(peak)-want) > 50 {
		t.Fatalf("peak %d, want about %.0f", peak, want)
	}
}

func abs(v int16) int16 {
	if v < 0 {
		return -v
	}
	return v
}
