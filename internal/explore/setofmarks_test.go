package explore

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/creativetoken/chromancer/internal/perceive"
)

func blankPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, draw)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDrawSetOfMarksMarksVisibleNodesAndDownscales(t *testing.T) {
	src := blankPNG(t, 400, 300)
	nodes := []perceive.Node{
		{Handle: 7, Visible: true, Rect: []int{10, 20, 50, 30}},
		{Handle: 12, Visible: true, Rect: []int{100, 100, 40, 40}},
		{Handle: 99, Visible: false, Rect: []int{0, 0, 10, 10}}, // skipped: not visible
		{Handle: 3, Visible: true, Rect: []int{5, 5, 0, 0}},     // skipped: zero area
	}
	out, marked, err := drawSetOfMarks(src, nodes, 1.0, 0)
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if len(marked) != 2 || marked[0] != 7 || marked[1] != 12 {
		t.Fatalf("marked = %v, want [7 12]", marked)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if img.Bounds().Dx() != 400 {
		t.Errorf("width = %d, want 400 (no downscale)", img.Bounds().Dx())
	}
	// Some pixel must now be the mark outline colour (the source was all white).
	found := false
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y && !found; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 == 255 && g>>8 == 40 && bl>>8 == 40 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("no mark outline drawn")
	}
}

func TestDrawSetOfMarksDPRAndDownscale(t *testing.T) {
	src := blankPNG(t, 2000, 1000) // device px (e.g. DPR 2 of a 1000-wide viewport)
	nodes := []perceive.Node{{Handle: 1, Visible: true, Rect: []int{100, 50, 80, 40}}}
	out, marked, err := drawSetOfMarks(src, nodes, 2.0, 1024)
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if len(marked) != 1 {
		t.Fatalf("marked = %v", marked)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() != 1024 {
		t.Errorf("downscaled width = %d, want 1024", img.Bounds().Dx())
	}
}

func TestItoa(t *testing.T) {
	for _, c := range []struct {
		n int
		s string
	}{{0, "0"}, {7, "7"}, {42, "42"}, {150, "150"}} {
		if got := itoa(c.n); got != c.s {
			t.Errorf("itoa(%d) = %q, want %q", c.n, got, c.s)
		}
	}
}
