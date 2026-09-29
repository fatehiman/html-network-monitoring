// Package icon draws the netmon icon: a rounded square in a status color with
// a white "pulse" line (like a heart-rate monitor). It is drawn in code with
// anti-aliasing, so no image files are needed.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Status colors — the same as the dots on the chart in the UI.
var (
	Green  = color.RGBA{0x2e, 0xcc, 0x71, 0xff} // < 500 ms
	Orange = color.RGBA{0xf3, 0x9c, 0x12, 0xff} // < 2000 ms
	Red    = color.RGBA{0xe7, 0x4c, 0x3c, 0xff} // >= 2000 ms or failed
	Grey   = color.RGBA{0x57, 0x65, 0x74, 0xff} // paused / no data yet
)

// pulse line, in 0..1 coordinates
var pulse = [][2]float64{{0.10, 0.56}, {0.30, 0.56}, {0.40, 0.28}, {0.54, 0.80}, {0.63, 0.42}, {0.70, 0.56}, {0.90, 0.56}}

// Draw returns a size×size image of the icon.
func Draw(bg color.RGBA, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	halfW := math.Max(0.8, s*0.05) // half line width in pixels
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			// rounded square
			a := clamp(0.5 - roundRectSDF(px, py, s*0.03, s*0.97, s*0.22))
			if a <= 0 {
				continue
			}
			// distance to the pulse line
			d := math.Inf(1)
			for i := 1; i < len(pulse); i++ {
				d = math.Min(d, segDist(px, py, pulse[i-1][0]*s, pulse[i-1][1]*s, pulse[i][0]*s, pulse[i][1]*s))
			}
			l := clamp(0.5 - (d - halfW))
			mix := func(c uint8) uint8 { return uint8(math.Round(float64(c)*(1-l) + 255*l)) }
			img.SetNRGBA(x, y, color.NRGBA{mix(bg.R), mix(bg.G), mix(bg.B), uint8(math.Round(a * 255))})
		}
	}
	return img
}

// PNG returns the icon as PNG bytes.
func PNG(bg color.RGBA, size int) []byte {
	var b bytes.Buffer
	png.Encode(&b, Draw(bg, size))
	return b.Bytes()
}

// ICO returns a Windows .ico file with the icon in several sizes (32-bit BMP entries).
func ICO(bg color.RGBA, sizes ...int) []byte {
	if len(sizes) == 0 {
		sizes = []int{16, 20, 24, 32, 48, 64}
	}
	var imgs [][]byte
	for _, sz := range sizes {
		imgs = append(imgs, bmpEntry(Draw(bg, sz)))
	}
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint16(0))
	le(uint16(1)) // type: icon
	le(uint16(len(sizes)))
	off := 6 + 16*len(sizes)
	for i, sz := range sizes {
		b.WriteByte(byte(sz % 256)) // 0 means 256
		b.WriteByte(byte(sz % 256))
		b.WriteByte(0) // palette
		b.WriteByte(0) // reserved
		le(uint16(1))  // planes
		le(uint16(32)) // bits per pixel
		le(uint32(len(imgs[i])))
		le(uint32(off))
		off += len(imgs[i])
	}
	for _, im := range imgs {
		b.Write(im)
	}
	return b.Bytes()
}

// bmpEntry encodes one image as BITMAPINFOHEADER + bottom-up BGRA pixels + empty AND mask.
func bmpEntry(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	maskRow := ((w + 31) / 32) * 4
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(40))
	le(int32(w))
	le(int32(h * 2)) // XOR + AND mask
	le(uint16(1))
	le(uint16(32))
	le(uint32(0)) // BI_RGB
	le(uint32(w*h*4 + maskRow*h))
	le(int32(0))
	le(int32(0))
	le(uint32(0))
	le(uint32(0))
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, maskRow*h))
	return b.Bytes()
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// roundRectSDF: signed distance (pixels) from p to a square [lo,hi]² with corner radius r.
func roundRectSDF(px, py, lo, hi, r float64) float64 {
	c := (lo + hi) / 2
	half := (hi-lo)/2 - r
	qx, qy := math.Abs(px-c)-half, math.Abs(py-c)-half
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - r
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := clamp(((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}
