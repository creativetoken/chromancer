package explore

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/creativetoken/chromancer/internal/perceive"
)

// Set-of-marks: overlay numbered boxes onto a viewport screenshot so a vision
// model can refer to a perceived DOM node by its handle number (and we can act
// on it by the node's rect-centre coordinate). Marks are drawn in Go on the
// captured PNG — never injected into the page — so they survive closed shadow
// roots and canvas UIs and never perturb the DOM.

var (
	markOutline = color.RGBA{R: 255, G: 40, B: 40, A: 255}  // box outline
	labelBG     = color.RGBA{R: 20, G: 20, B: 20, A: 255}   // number background
	labelFG     = color.RGBA{R: 255, G: 235, B: 59, A: 255} // number ink
)

// digits3x5 is a tiny bitmap font (3 wide, 5 tall) for 0-9, so set-of-marks
// needs no external font dependency.
var digits3x5 = map[rune][5]string{
	'0': {"111", "101", "101", "101", "111"},
	'1': {"010", "110", "010", "010", "111"},
	'2': {"111", "001", "111", "100", "111"},
	'3': {"111", "001", "111", "001", "111"},
	'4': {"101", "101", "111", "001", "001"},
	'5': {"111", "100", "111", "001", "111"},
	'6': {"111", "100", "111", "101", "111"},
	'7': {"111", "001", "010", "100", "100"},
	'8': {"111", "101", "111", "101", "111"},
	'9': {"111", "101", "111", "001", "111"},
}

// pngDims returns a PNG's pixel dimensions from its header, or 0,0 on error.
func pngDims(b []byte) (int, int) {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// drawSetOfMarks overlays a numbered box for every visible node that has a rect
// onto the screenshot PNG. dpr scales CSS-px rects to the screenshot's device
// pixels. maxW downscales the result (nearest-neighbour) to cap image tokens; 0
// disables the cap. It returns the marked PNG and the handles that were drawn.
func drawSetOfMarks(pngBytes []byte, nodes []perceive.Node, dpr float64, maxW int) ([]byte, []int, error) {
	src, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, nil, err
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	if dpr <= 0 {
		dpr = 1
	}
	var marked []int
	for _, n := range nodes {
		if !n.Visible || len(n.Rect) != 4 {
			continue
		}
		x := b.Min.X + int(float64(n.Rect[0])*dpr)
		y := b.Min.Y + int(float64(n.Rect[1])*dpr)
		w := int(float64(n.Rect[2]) * dpr)
		h := int(float64(n.Rect[3]) * dpr)
		if w <= 0 || h <= 0 {
			continue
		}
		drawRectOutline(dst, x, y, w, h)
		drawLabel(dst, x, y, n.Handle)
		marked = append(marked, n.Handle)
	}
	var out image.Image = dst
	if maxW > 0 && b.Dx() > maxW {
		out = downscale(dst, maxW)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), marked, nil
}

// drawRectOutline draws a 2px outline of the box, clipped to the image.
func drawRectOutline(img *image.RGBA, x, y, w, h int) {
	const t = 2
	set := func(px, py int) {
		if image.Pt(px, py).In(img.Bounds()) {
			img.SetRGBA(px, py, markOutline)
		}
	}
	for i := 0; i < w; i++ {
		for j := 0; j < t; j++ {
			set(x+i, y+j)
			set(x+i, y+h-1-j)
		}
	}
	for i := 0; i < h; i++ {
		for j := 0; j < t; j++ {
			set(x+j, y+i)
			set(x+w-1-j, y+i)
		}
	}
}

// drawLabel paints the handle number at the box's top-left in a small font on a
// solid tag, nudged to stay inside the image.
func drawLabel(img *image.RGBA, x, y, handle int) {
	const scale = 3     // px per font cell
	const dw, dh = 3, 5 // font cell dims
	const pad = 2       // tag padding
	s := []rune(itoa(handle))
	glyphW := dw*scale + 1 // one-column gap between digits
	tagW := pad*2 + len(s)*glyphW
	tagH := pad*2 + dh*scale
	// Keep the tag on-screen.
	bnd := img.Bounds()
	if x < bnd.Min.X {
		x = bnd.Min.X
	}
	if y < bnd.Min.Y {
		y = bnd.Min.Y
	}
	if x+tagW > bnd.Max.X {
		x = bnd.Max.X - tagW
	}
	if y+tagH > bnd.Max.Y {
		y = bnd.Max.Y - tagH
	}
	draw.Draw(img, image.Rect(x, y, x+tagW, y+tagH), &image.Uniform{labelBG}, image.Point{}, draw.Src)
	cx := x + pad
	for _, r := range s {
		g, ok := digits3x5[r]
		if ok {
			for row := 0; row < dh; row++ {
				for col := 0; col < dw; col++ {
					if g[row][col] == '1' {
						px0 := cx + col*scale
						py0 := y + pad + row*scale
						for a := 0; a < scale; a++ {
							for bb := 0; bb < scale; bb++ {
								p := image.Pt(px0+a, py0+bb)
								if p.In(bnd) {
									img.SetRGBA(p.X, p.Y, labelFG)
								}
							}
						}
					}
				}
			}
		}
		cx += glyphW
	}
}

// itoa renders a non-negative int without importing strconv for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// downscale resizes img to width maxW (preserving aspect) by nearest-neighbour.
func downscale(src *image.RGBA, maxW int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= maxW || sw == 0 {
		return src
	}
	dw := maxW
	dh := sh * maxW / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for yy := 0; yy < dh; yy++ {
		syy := b.Min.Y + yy*sh/dh
		for xx := 0; xx < dw; xx++ {
			sxx := b.Min.X + xx*sw/dw
			dst.SetRGBA(xx, yy, src.RGBAAt(sxx, syy))
		}
	}
	return dst
}
