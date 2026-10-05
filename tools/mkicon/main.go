// Mkicon draws GoRex's icon, resources/icon.png: the window's gradient in
// the squircle of macOS icons, with panes on it as the app splits them.
//
//	go run ./tools/mkicon
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"

	"golang.org/x/image/vector"
)

const size = 1024

func main() {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// The squircle of macOS's icon grid: 824 wide, inset by 100.
	x0, y0, w := float32(100), float32(100), float32(824)
	shadow(img, x0+6, y0+12, w-12, w-12, 186, 20, color.RGBA{60, 30, 50, 46})
	grad := gradient(size, []stop{{0, rgb(0xf7e4f6)}, {0.45, rgb(0xeedbe3)}, {0.7, rgb(0xeeddd0)}, {1, rgb(0xebdcb2)}})
	fill(img, squircle(x0, y0, w, w, 186), grad)
	// The panes: one left above another, one right, as the app shows them.
	pad, gap := float32(70), float32(36)
	inner := w - 2*pad
	lw := inner*0.54 - gap/2
	th := inner*0.5 - gap/2
	cards := []struct {
		x, y, w, h float32
		a          uint8
	}{
		{x0 + pad, y0 + pad + 40, lw, th, 250},
		{x0 + pad, y0 + pad + 40 + th + gap, lw, inner - th - gap - 40, 175},
		{x0 + pad + lw + gap, y0 + pad + 40, inner - lw - gap, inner - 40, 175},
	}
	for _, c := range cards {
		shadow(img, c.x, c.y+8, c.w, c.h, 44, 22, color.RGBA{80, 40, 60, 40})
		fill(img, rounded(c.x, c.y, c.w, c.h, 44), image.NewUniform(color.NRGBA{255, 255, 255, c.a}))
	}
	// A prompt on the first pane: a chevron and a cursor.
	c := cards[0]
	ink := image.NewUniform(color.NRGBA{42, 45, 49, 255})
	green := image.NewUniform(color.NRGBA{78, 142, 95, 255})
	cx, cy := c.x+68, c.y+c.h/2-6
	for _, r := range stroke([][2]float32{{cx, cy - 58}, {cx + 62, cy}, {cx, cy + 58}}, 26) {
		fill(img, r, green)
	}
	fill(img, rounded(cx+100, cy+34, 104, 26, 13), ink)
	// Snake dots on the right pane.
	r := cards[2]
	for i := 0; i < 4; i++ {
		fill(img, rounded(r.x+52+float32(i)*44, r.y+r.h*0.42, 30, 30, 7), image.NewUniform(color.NRGBA{86, 141, 102, 255}))
	}
	fill(img, rounded(r.x+52+3*44, r.y+r.h*0.42+44, 30, 30, 7), image.NewUniform(color.NRGBA{86, 141, 102, 255}))
	fill(img, rounded(r.x+r.w-100, r.y+r.h*0.7, 34, 34, 17), image.NewUniform(color.NRGBA{188, 83, 98, 255}))
	// Lines of text on the lower left pane.
	l := cards[1]
	for i, lw := range []float32{0.62, 0.44, 0.7} {
		col := color.NRGBA{120, 120, 128, 150}
		if i == 1 {
			col = color.NRGBA{194, 70, 90, 170}
		}
		fill(img, rounded(l.x+52, l.y+56+float32(i)*48, (l.w-104)*lw, 22, 11), image.NewUniform(col))
	}
	if err := os.MkdirAll("resources", 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create("resources/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}

func rgb(v uint32) color.NRGBA {
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

type stop struct {
	at float64
	c  color.NRGBA
}

// gradient is a vertical gradient through stops.
func gradient(h int, stops []stop) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 1, h))
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h-1)
		i := 0
		for i < len(stops)-2 && t > stops[i+1].at {
			i++
		}
		a, b := stops[i], stops[i+1]
		f := math.Max(0, math.Min(1, (t-a.at)/(b.at-a.at)))
		mix := func(p, q uint8) uint8 { return uint8(float64(p) + (float64(q)-float64(p))*f + 0.5) }
		img.SetNRGBA(0, y, color.NRGBA{mix(a.c.R, b.c.R), mix(a.c.G, b.c.G), mix(a.c.B, b.c.B), 255})
	}
	return &stretch{img}
}

// stretch repeats a one-pixel-wide image across.
type stretch struct{ *image.NRGBA }

func (s *stretch) Bounds() image.Rectangle { return image.Rect(0, 0, size, size) }
func (s *stretch) At(x, y int) color.Color { return s.NRGBA.At(0, y) }
func (s *stretch) ColorModel() color.Model { return color.NRGBAModel }

func fill(dst *image.RGBA, r *vector.Rasterizer, src image.Image) {
	r.Draw(dst, dst.Bounds(), src, image.Point{})
}

// rounded is a rounded rectangle.
func rounded(x, y, w, h, rad float32) *vector.Rasterizer {
	r := vector.NewRasterizer(size, size)
	k := rad * 0.5523
	r.MoveTo(x+rad, y)
	r.LineTo(x+w-rad, y)
	r.CubeTo(x+w-rad+k, y, x+w, y+rad-k, x+w, y+rad)
	r.LineTo(x+w, y+h-rad)
	r.CubeTo(x+w, y+h-rad+k, x+w-rad+k, y+h, x+w-rad, y+h)
	r.LineTo(x+rad, y+h)
	r.CubeTo(x+rad-k, y+h, x, y+h-rad+k, x, y+h-rad)
	r.LineTo(x, y+rad)
	r.CubeTo(x, y+rad-k, x+rad-k, y, x+rad, y)
	r.ClosePath()
	return r
}

// squircle is a rounded rectangle of continuous curvature, as macOS's
// icons: a superellipse.
func squircle(x, y, w, h, _ float32) *vector.Rasterizer {
	r := vector.NewRasterizer(size, size)
	const n = 5.0
	cx, cy, a, b := float64(x+w/2), float64(y+h/2), float64(w/2), float64(h/2)
	for i := 0; i <= 720; i++ {
		t := float64(i) / 720 * 2 * math.Pi
		c, s := math.Cos(t), math.Sin(t)
		px := cx + a*math.Copysign(math.Pow(math.Abs(c), 2/n), c)
		py := cy + b*math.Copysign(math.Pow(math.Abs(s), 2/n), s)
		if i == 0 {
			r.MoveTo(float32(px), float32(py))
		} else {
			r.LineTo(float32(px), float32(py))
		}
	}
	r.ClosePath()
	return r
}

// stroke is a polyline of a width, with round joins and caps: shapes to
// fill one by one, as their windings would cancel where they overlap.
func stroke(pts [][2]float32, width float32) []*vector.Rasterizer {
	var out []*vector.Rasterizer
	hw := width / 2
	for i := 0; i+1 < len(pts); i++ {
		r := vector.NewRasterizer(size, size)
		out = append(out, r)
		a, b := pts[i], pts[i+1]
		dx, dy := b[0]-a[0], b[1]-a[1]
		l := float32(math.Hypot(float64(dx), float64(dy)))
		nx, ny := -dy/l*hw, dx/l*hw
		r.MoveTo(a[0]+nx, a[1]+ny)
		r.LineTo(b[0]+nx, b[1]+ny)
		r.LineTo(b[0]-nx, b[1]-ny)
		r.LineTo(a[0]-nx, a[1]-ny)
		r.ClosePath()
	}
	for _, p := range pts {
		r := vector.NewRasterizer(size, size)
		circle(r, p[0], p[1], hw)
		out = append(out, r)
	}
	return out
}

func circle(r *vector.Rasterizer, cx, cy, rad float32) {
	k := rad * 0.5523
	r.MoveTo(cx+rad, cy)
	r.CubeTo(cx+rad, cy+k, cx+k, cy+rad, cx, cy+rad)
	r.CubeTo(cx-k, cy+rad, cx-rad, cy+k, cx-rad, cy)
	r.CubeTo(cx-rad, cy-k, cx-k, cy-rad, cx, cy-rad)
	r.CubeTo(cx+k, cy-rad, cx+rad, cy-k, cx+rad, cy)
	r.ClosePath()
}

// shadow draws a soft shadow of a rounded rectangle, as layers of a
// little alpha each, growing.
func shadow(dst *image.RGBA, x, y, w, h, rad, blur float32, c color.RGBA) {
	steps := 12
	for i := steps; i >= 1; i-- {
		g := blur * float32(i) / float32(steps)
		a := float64(c.A) / float64(steps) * 0.9
		col := color.NRGBA{c.R, c.G, c.B, uint8(a)}
		m := rounded(x-g/2, y-g/2, w+g, h+g, rad+g/2)
		m.DrawOp = draw.Over
		fill(dst, m, image.NewUniform(col))
	}
}
