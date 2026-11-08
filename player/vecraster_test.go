package main

// vecraster_test.go — the icon rasterizer's geometry, checked on coverage
// values alone: no renderer, no SDL.

import (
	"math"
	"testing"
)

// vecSum is the total coverage of the canvas, i.e. the painted area in pixels.
func vecSum(c *vecCanvas) float64 {
	sum := 0.0
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			sum += c.Coverage(x, y)
		}
	}
	return sum
}

func vecNear(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func vecFill(c *vecCanvas, p *vecPath, evenOdd bool) {
	c.Paint(p, vecPaint{EvenOdd: evenOdd, Opacity: 1})
}

func TestVecFillPixelAlignedSquareIsExact(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(10, 10)
	p.RoundRect(2, 2, 5, 5, 0)
	vecFill(&c, &p, false)
	if got := vecSum(&c); !vecNear(got, 25, 1e-4) {
		t.Fatalf("area = %v, want 25", got)
	}
	for _, tc := range []struct {
		x, y int
		want float64
	}{
		{2, 2, 1}, {6, 6, 1}, {7, 7, 0}, {1, 3, 0}, {4, 1, 0},
	} {
		if got := c.Coverage(tc.x, tc.y); !vecNear(got, tc.want, 1e-4) {
			t.Errorf("coverage(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestVecFillHalfPixelEdgesAreAreaCoverage(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(10, 10)
	p.RoundRect(2.5, 2.5, 4, 4, 0)
	vecFill(&c, &p, false)
	if got := vecSum(&c); !vecNear(got, 16, 1e-4) {
		t.Fatalf("area = %v, want 16", got)
	}
	for _, tc := range []struct {
		x, y int
		want float64
	}{
		{2, 2, 0.25}, {3, 2, 0.5}, {3, 3, 1}, {6, 6, 0.25}, {6, 4, 0.5},
	} {
		if got := c.Coverage(tc.x, tc.y); !vecNear(got, tc.want, 1e-3) {
			t.Errorf("coverage(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestVecFillCircleAreaAndFillRules(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(32, 32)
	p.Circle(16, 16, 10)
	vecFill(&c, &p, false)
	if got, want := vecSum(&c), math.Pi*100; math.Abs(got-want)/want > 0.005 {
		t.Fatalf("disc area = %v, want %v within 0.5%%", got, want)
	}

	// Two concentric circles drawn the same way round: non-zero fills the
	// middle, even-odd makes a ring.
	p.Reset()
	p.Circle(16, 16, 10)
	p.Circle(16, 16, 5)
	c.Reset(32, 32)
	vecFill(&c, &p, false)
	if got := c.Coverage(16, 16); got < 0.999 {
		t.Errorf("non-zero centre = %v, want filled", got)
	}
	c.Reset(32, 32)
	vecFill(&c, &p, true)
	if got := c.Coverage(16, 16); got > 0.001 {
		t.Errorf("even-odd centre = %v, want a hole", got)
	}
	if got := c.Coverage(23, 16); got < 0.999 {
		t.Errorf("even-odd band = %v, want filled", got)
	}
	if got, want := vecSum(&c), math.Pi*75; math.Abs(got-want)/want > 0.005 {
		t.Errorf("ring area = %v, want %v within 0.5%%", got, want)
	}
}

func TestVecStrokeCaps(t *testing.T) {
	cases := []struct {
		name  string
		cap   vecCap
		area  float64
		edge3 float64 // coverage of pixel (3,7), half a pixel before the start
	}{
		{"butt", vecCapButt, 16, 0},
		{"square", vecCapSquare, 20, 1},
		{"round", vecCapRound, 16 + math.Pi, 0.793},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c vecCanvas
			var p vecPath
			c.Reset(16, 16)
			p.MoveTo(4, 8)
			p.LineTo(12, 8)
			c.Paint(&p, vecPaint{Stroke: true, Width: 2, Cap: tc.cap, Opacity: 1})
			if got := vecSum(&c); !vecNear(got, tc.area, 0.05) {
				t.Errorf("area = %v, want %v", got, tc.area)
			}
			// A 2px stroke centred on a pixel edge is two solid rows.
			for x := 4; x < 12; x++ {
				if c.Coverage(x, 7) < 0.999 || c.Coverage(x, 8) < 0.999 {
					t.Fatalf("pixel column %d is not solid in rows 7-8", x)
				}
				if c.Coverage(x, 6) > 0.001 || c.Coverage(x, 9) > 0.001 {
					t.Fatalf("pixel column %d bleeds outside rows 7-8", x)
				}
			}
			if got := c.Coverage(3, 7); !vecNear(got, tc.edge3, 0.01) {
				t.Errorf("cap pixel (3,7) = %v, want %v", got, tc.edge3)
			}
		})
	}
}

func TestVecStrokeDiagonalAndDot(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(16, 16)
	p.MoveTo(2, 2)
	p.LineTo(12, 12)
	c.Paint(&p, vecPaint{Stroke: true, Width: 2, Cap: vecCapButt, Opacity: 1})
	if got, want := vecSum(&c), math.Hypot(10, 10)*2; math.Abs(got-want)/want > 0.02 {
		t.Errorf("diagonal stroke area = %v, want %v within 2%%", got, want)
	}

	// A lone point strokes as a dot the width of the stroke.
	p.Reset()
	p.MoveTo(5, 5)
	c.Reset(10, 10)
	c.Paint(&p, vecPaint{Stroke: true, Width: 4, Opacity: 1})
	if got, want := vecSum(&c), math.Pi*4; math.Abs(got-want) > 0.6 {
		t.Errorf("dot area = %v, want about %v", got, want)
	}
}

func TestVecCompositing(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(4, 4)
	p.RoundRect(0, 0, 4, 4, 0)
	c.Paint(&p, vecPaint{Opacity: 0.3})
	c.Paint(&p, vecPaint{Opacity: 0.3})
	if got := c.Coverage(1, 1); !vecNear(got, 0.51, 1e-4) {
		t.Errorf("two 30%% layers = %v, want 0.51 (over compositing)", got)
	}

	c.Reset(10, 10)
	p.Reset()
	p.RoundRect(1, 1, 8, 8, 0)
	vecFill(&c, &p, false)
	p.Reset()
	p.RoundRect(3, 3, 4, 4, 0)
	c.Paint(&p, vecPaint{Opacity: 1, Erase: true})
	if got := c.Coverage(4, 4); got > 0.001 {
		t.Errorf("erased pixel = %v, want 0", got)
	}
	if got := vecSum(&c); !vecNear(got, 48, 1e-3) {
		t.Errorf("area after the knock-out = %v, want 48", got)
	}

	// Nothing is painted at zero opacity, and Paint tolerates empty input.
	c.Reset(4, 4)
	c.Paint(&p, vecPaint{Opacity: 0})
	c.Paint(nil, vecPaint{Opacity: 1})
	var empty vecPath
	c.Paint(&empty, vecPaint{Opacity: 1})
	c.Paint(&empty, vecPaint{Stroke: true, Width: 2, Opacity: 1})
	if got := vecSum(&c); got != 0 {
		t.Errorf("area = %v, want 0", got)
	}
}

func TestVecClipsToCanvas(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(8, 8)
	p.RoundRect(-20, -20, 60, 60, 4)
	p.Circle(30, 30, 50)
	vecFill(&c, &p, false)
	p.Reset()
	p.MoveTo(-100, 4)
	p.LineTo(100, 4)
	c.Paint(&p, vecPaint{Stroke: true, Width: 3, Cap: vecCapSquare, Opacity: 1})
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if v := c.Coverage(x, y); v < 0 || v > 1 || math.IsNaN(v) {
				t.Fatalf("coverage(%d,%d) = %v out of range", x, y, v)
			}
		}
	}
	// A zero-sized canvas paints nothing and does not panic.
	c.Reset(0, 0)
	vecFill(&c, &p, false)
	var dst [4]uint8
	c.AlphaBytes(dst[:])
}

func TestVecArcAndCurveEndpoints(t *testing.T) {
	var p vecPath
	p.Arc(10, 10, 5, 0, 90)
	first, last := p.pts[0], p.pts[len(p.pts)-1]
	if !vecNear(first.X, 15, 1e-9) || !vecNear(first.Y, 10, 1e-9) {
		t.Errorf("arc starts at %+v, want (15,10)", first)
	}
	if !vecNear(last.X, 10, 1e-9) || !vecNear(last.Y, 15, 1e-9) {
		t.Errorf("arc ends at %+v, want (10,15): angles run clockwise on screen", last)
	}

	p.Reset()
	p.MoveTo(0, 0)
	p.QuadTo(5, 10, 10, 0)
	p.CubicTo(12, 4, 18, 4, 20, 0)
	end := p.pts[len(p.pts)-1]
	if !vecNear(end.X, 20, 1e-9) || !vecNear(end.Y, 0, 1e-9) {
		t.Errorf("curves end at %+v, want (20,0)", end)
	}
	if len(p.starts) != 1 {
		t.Errorf("curves made %d subpaths, want one", len(p.starts))
	}

	// End leaves the subpath open; the next arc starts a new one.
	p.Reset()
	p.Arc(10, 10, 5, 0, 90)
	p.End()
	p.Arc(10, 10, 8, 0, 90)
	if len(p.starts) != 2 || p.closed[0] || p.closed[1] {
		t.Errorf("got %d subpaths (closed %v), want two open arcs", len(p.starts), p.closed)
	}
}

func TestVecAlphaBytes(t *testing.T) {
	var c vecCanvas
	var p vecPath
	c.Reset(10, 10)
	p.RoundRect(2.5, 2.5, 4, 4, 0)
	vecFill(&c, &p, false)
	buf := make([]uint8, 100)
	c.AlphaBytes(buf)
	for _, tc := range []struct {
		x, y int
		want uint8
	}{
		{3, 3, 255}, {2, 2, 64}, {3, 2, 128}, {0, 0, 0},
	} {
		if got := buf[tc.y*10+tc.x]; got != tc.want {
			t.Errorf("alpha(%d,%d) = %d, want %d", tc.x, tc.y, got, tc.want)
		}
	}
}
