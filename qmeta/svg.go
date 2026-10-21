package qmeta

import (
	"fmt"
	"io"
	"math"
)

// SVG draws the chain's field: an arrow at each grid point in the most
// likely direction of the dynamics, shaded by the drift's magnitude, with
// the queue length across and the orbit up (the papers' visualization).
// Where the arrows turn up and right, the system is drawn into a retry
// storm; where they point down and left, it drains.
func (c *Chain) SVG(w io.Writer, stepU, stepV int) error {
	const W, H, pad = 640, 420, 40
	field := c.Field(stepU, stepV)
	maxMag := 0.0
	for _, d := range field {
		maxMag = math.Max(maxMag, math.Hypot(d.DU, d.DV))
	}
	x := func(u float64) float64 { return pad + u/float64(c.N)*(W-2*pad) }
	y := func(v float64) float64 { return H - pad - v/float64(c.V)*(H-2*pad) }
	cell := math.Min(float64(stepU)/float64(c.N)*(W-2*pad), float64(stepV)/float64(c.V)*(H-2*pad)) * 0.45
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="sans-serif" font-size="11">`+"\n", W, H)
	fmt.Fprintf(w, `<rect width="%d" height="%d" fill="white"/>`+"\n", W, H)
	fmt.Fprintf(w, `<text x="%d" y="%d" text-anchor="middle">queue length (u)</text>`+"\n", W/2, H-8)
	fmt.Fprintf(w, `<text x="12" y="%d" transform="rotate(-90 12 %d)" text-anchor="middle">orbit (v)</text>`+"\n", H/2, H/2)
	fmt.Fprintf(w, `<text x="%d" y="16" text-anchor="middle">λ=%.2g μ=%.2g timeout=%.2gs retries=%d</text>`+"\n", W/2, c.P.Lambda, c.P.Mu, c.P.Timeout, c.P.Retries)
	for _, t := range []int{0, c.N / 2, c.N} {
		fmt.Fprintf(w, `<text x="%.0f" y="%d" text-anchor="middle">%d</text>`+"\n", x(float64(t)), H-pad+14, t)
	}
	for _, t := range []int{0, c.V / 2, c.V} {
		fmt.Fprintf(w, `<text x="%d" y="%.0f" text-anchor="end">%d</text>`+"\n", pad-4, y(float64(t))+4, t)
	}
	for _, d := range field {
		mag := math.Hypot(d.DU, d.DV)
		if mag == 0 {
			continue
		}
		// screen direction (orbit grows upward)
		dx, dy := d.DU/mag, -d.DV/mag
		x0, y0 := x(float64(d.U)), y(float64(d.V))
		x1, y1 := x0+dx*cell, y0+dy*cell
		shade := math.Log1p(mag) / math.Log1p(maxMag)
		col := fmt.Sprintf("hsl(%.0f,70%%,%.0f%%)", 240-200*shade, 35+20*shade)
		fmt.Fprintf(w, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.4"/>`, x0, y0, x1, y1, col)
		// arrow head
		ax, ay := -dx*cell*0.35, -dy*cell*0.35
		fmt.Fprintf(w, `<polyline points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="none" stroke="%s" stroke-width="1.4"/>`+"\n",
			x1+ax-ay*0.5, y1+ay+ax*0.5, x1, y1, x1+ax+ay*0.5, y1+ay-ax*0.5, col)
	}
	_, err := fmt.Fprintln(w, "</svg>")
	return err
}
