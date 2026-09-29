// Command iconpreview writes a PNG sheet of all icon colors and sizes (for checking the design).
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"

	"netmon/internal/icon"
)

func main() {
	sizes := []int{16, 24, 32, 64, 128}
	cols := []color.RGBA{icon.Green, icon.Orange, icon.Red, icon.Grey}
	sheet := image.NewNRGBA(image.Rect(0, 0, 4*140, 5*140))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.RGBA{0x20, 0x20, 0x20, 255}}, image.Point{}, draw.Src)
	for i, c := range cols {
		for j, s := range sizes {
			im := icon.Draw(c, s)
			draw.Draw(sheet, image.Rect(i*140+6, j*140+6, i*140+6+s, j*140+6+s), im, image.Point{}, draw.Over)
		}
	}
	f, _ := os.Create(os.Args[1])
	png.Encode(f, sheet)
	f.Close()
}
