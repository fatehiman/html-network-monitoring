// Package sound makes short beeps as WAV data: a sine wave with a smooth
// fade-in and fade-out. The Windows Beep() API makes a square wave that starts
// and stops at full volume, which sounds like a "click"/buzz; this does not.
package sound

import (
	"bytes"
	"encoding/binary"
	"math"
)

const (
	SampleRate = 44100
	Volume     = 0.25 // 0..1
	fadeMs     = 5    // fade-in and fade-out length
	leadMs     = 15   // silence before the tone, lets the audio device wake up
	tailMs     = 10   // silence after the tone
)

// WAV returns a 16-bit mono WAV file with a freq-Hz tone of ms milliseconds.
func WAV(freq float64, ms int) []byte {
	n := func(ms int) int { return SampleRate * ms / 1000 }
	lead, tone, tail, fade := n(leadMs), n(ms), n(tailMs), n(fadeMs)
	samples := make([]int16, lead+tone+tail)
	for i := 0; i < tone; i++ {
		env := 1.0
		if i < fade {
			env = 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(fade)) // raised cosine
		} else if k := tone - 1 - i; k < fade {
			env = 0.5 - 0.5*math.Cos(math.Pi*float64(k)/float64(fade))
		}
		v := Volume * env * math.Sin(2*math.Pi*freq*float64(i)/SampleRate)
		samples[lead+i] = int16(math.Round(v * 32767))
	}

	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	dataLen := uint32(len(samples) * 2)
	b.WriteString("RIFF")
	le(36 + dataLen)
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1)) // PCM
	le(uint16(1)) // mono
	le(uint32(SampleRate))
	le(uint32(SampleRate * 2)) // byte rate
	le(uint16(2))              // block align
	le(uint16(16))             // bits per sample
	b.WriteString("data")
	le(dataLen)
	le(samples)
	return b.Bytes()
}
