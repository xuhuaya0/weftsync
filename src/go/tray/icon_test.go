//go:build windows

package tray

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
)

// The icon is drawn with raw pixels, so two mistakes are invisible to a
// "Start returned nil" test: swapped colour channels and a vertically flipped
// buffer. Both produce a valid icon that simply looks wrong.
func TestIconPixels(t *testing.T) {
	const size = iconSize
	pix := make([]byte, size*size*4)
	fillIconPixels(pix, size)

	at := func(x, y int) color.RGBA {
		i := (y*size + x) * 4
		return color.RGBA{R: pix[i+2], G: pix[i+1], B: pix[i+0], A: pix[i+3]}
	}

	// Corners are outside the disc, so they must stay fully transparent.
	for _, p := range [][2]int{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
		if a := at(p[0], p[1]).A; a != 0 {
			t.Errorf("corner (%d,%d) alpha = %d, want 0", p[0], p[1], a)
		}
	}

	// The disc is blue: BGRA in the buffer, so the red channel must be the low
	// value. Sampling off the arrow avoids the glyph.
	c := at(size/4, size/2+6)
	if c.A != 0xff {
		t.Fatalf("disc is transparent at (%d,%d)", size/4, size/2+6)
	}
	if c.B <= c.R {
		t.Errorf("disc colour %v is not blue; channels may be swapped", c)
	}

	// The arrow is white and sits horizontally across the middle.
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	if got := at(size/2, size/2); got != white {
		t.Errorf("centre pixel = %v, want white (the arrow shaft)", got)
	}
	// A point just above the shaft must still be disc, which proves the glyph is
	// bounded rather than covering the middle band.
	if got := at(size/2, size/2-2); got == white {
		t.Error("the arrow is thicker than intended")
	}

	// The arrow must not be mirrored: the head is on the right, so a row above
	// the shaft is disc-only on the left and white under the head.
	//
	// The shaft spans mid-1..mid+1, so a sample at mid-1 would be white on both
	// sides and prove nothing. mid-2 is outside it.
	if got := at(10, size/2-2); got == white {
		t.Error("the arrow shaft is thicker than intended; sample was not above it")
	}
	if got := at(size-11, size/2-2); got != white {
		t.Errorf("the arrow head is missing on the right at (%d,%d)", size-11, size/2-2)
	}

	// Write a preview when asked, so the shape can be looked at rather than
	// only asserted on.
	if out := os.Getenv("WEFTSYNC_ICON_PNG"); out != "" {
		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				img.SetNRGBA(x, y, color.NRGBA(at(x, y)))
			}
		}
		f, err := os.Create(out)
		if err != nil {
			t.Fatalf("create %s: %v", out, err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		t.Logf("wrote icon preview to %s", out)
	}
}
