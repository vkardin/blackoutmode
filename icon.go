package main

import "math"

// The bolt of the logo, enlarged to fill a small tray badge.
var boltPoly = [][2]float64{
	{0.573, 0.125}, {0.271, 0.552}, {0.458, 0.552}, {0.406, 0.875}, {0.729, 0.427}, {0.521, 0.427},
}

func inPoly(x, y float64, p [][2]float64) bool {
	in := false
	j := len(p) - 1
	for i := range p {
		xi, yi := p[i][0], p[i][1]
		xj, yj := p[j][0], p[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
		j = i
	}
	return in
}

// RenderIcon draws the tray badge: a rounded square with the bolt.
// kind: 0 idle (slate, white bolt), 1 blackout mode (amber, dark bolt),
// 2 game mode (coral), 3 film mode (blue).
// Returns size*size premultiplied BGRA pixels, top-down.
func RenderIcon(size int, kind int) []byte {
	const ss = 4
	top, bottom := [3]float64{0x4a, 0x52, 0x5c}, [3]float64{0x3a, 0x41, 0x4a} // R,G,B
	fg := [3]float64{255, 255, 255}
	switch kind {
	case 1:
		top, bottom = [3]float64{0xff, 0xd9, 0x78}, [3]float64{0xf2, 0xa5, 0x31}
		fg = [3]float64{0x1a, 0x13, 0x00}
	case 2:
		top, bottom = [3]float64{0xff, 0x8f, 0x6b}, [3]float64{0xf0, 0x5a, 0x3c}
	case 3:
		top, bottom = [3]float64{0x6f, 0xb0, 0xff}, [3]float64{0x2f, 0x7c, 0xf0}
	}
	const radius = 0.26 // of the side
	out := make([]byte, size*size*4)
	n := float64(size * ss)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px*ss+sx) + 0.5) / n
					y := (float64(py*ss+sy) + 0.5) / n
					dx := math.Max(math.Max(radius-x, x-(1-radius)), 0)
					dy := math.Max(math.Max(radius-y, y-(1-radius)), 0)
					if math.Hypot(dx, dy) > radius {
						continue
					}
					c := [3]float64{top[0] + (bottom[0]-top[0])*y, top[1] + (bottom[1]-top[1])*y, top[2] + (bottom[2]-top[2])*y}
					if inPoly(x, y, boltPoly) {
						c = fg
					}
					r += c[0]
					g += c[1]
					b += c[2]
					a += 255
				}
			}
			k := float64(ss * ss)
			o := (py*size + px) * 4
			out[o+0] = byte(b/k + 0.5)
			out[o+1] = byte(g/k + 0.5)
			out[o+2] = byte(r/k + 0.5)
			out[o+3] = byte(a/k + 0.5)
		}
	}
	return out
}
