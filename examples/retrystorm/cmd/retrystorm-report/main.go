// Command retrystorm-report writes the metastability report of
// examples/retrystorm: per retry policy, the CTMC's field and predicted
// recovery time, and a simulated run through a load spike. With -steps it
// also writes the retry budget's steps over the Burst scenario, for
// quintgo validate.
package main

import (
	"flag"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/casselc/quint-connect-go/examples/retrystorm"
	"github.com/casselc/quint-connect-go/examples/retrystorm/retry"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
	"github.com/casselc/quint-connect-go/qobs"
)

func main() {
	out := flag.String("out", "report", "directory to write the report to")
	lambda := flag.Float64("lambda", 9, "base load (requests/s)")
	steps := flag.String("steps", "", "also record the budget's steps over the Burst scenario to this file")
	mutation := flag.String("mutation", "", "budget mutation for -steps: uncapped | deposit-on-retry")
	flag.Parse()
	if *steps != "" {
		if err := record(*steps, *mutation); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	var rows strings.Builder
	for _, p := range retrystorm.Policies {
		name := strings.ReplaceAll(p.Name, "+", "-")
		c := qmeta.New(p.Params(*lambda))
		if err := write(filepath.Join(*out, "field-"+name+".svg"), func(f *os.File) error { return c.SVG(f, 10, 12) }); err != nil {
			log.Fatal(err)
		}
		r := qload.Recovers(p.Config(*lambda), *lambda, retrystorm.Spike, 300e9)
		if err := write(filepath.Join(*out, "run-"+name+".svg"), func(f *os.File) error { return series(f, r) }); err != nil {
			log.Fatal(err)
		}
		pred := retrystorm.RecoveryTime(p, *lambda)
		n := retrystorm.Recoveries(p, *lambda, retrystorm.Spike, 6)
		fmt.Printf("%-15s predicted recovery %9.3g s; %d/6 simulated runs recover after %v\n", p.Name, pred, n, retrystorm.Spike)
		fmt.Fprintf(&rows, "<tr><td>%s</td><td>%.3g s</td><td>%d/6</td><td><img src=\"field-%s.svg\"></td><td><img src=\"run-%s.svg\"></td></tr>\n",
			html.EscapeString(p.Name), pred, n, name, name)
	}
	page := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><title>Retry storm report</title>
<style>body{font-family:sans-serif;margin:16px}img{width:420px;max-width:100%%}td{vertical-align:top;padding:4px}</style>
<h1>Retry policies at %.1f requests/s</h1>
<p>One server at %d requests/s with a %d-request queue; clients time out after %v and retry up to %d times.
Predicted recovery: the CTMC's expected time to drain from a full queue with 30 requests in the orbit.
Simulated: runs (seeds 1-6) whose goodput is back within 5 minutes of the spike (%v).</p>
<table><tr><th>policy</th><th>predicted recovery</th><th>simulated recoveries</th><th>CTMC field (queue across, orbit up)</th><th>one simulated run</th></tr>
%s</table>`, *lambda, retrystorm.ServiceRate, retrystorm.QueueCap, retrystorm.Timeout, retrystorm.MaxRetries, retrystorm.Spike, rows.String())
	if err := os.WriteFile(filepath.Join(*out, "index.html"), []byte(page), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("report:", filepath.Join(*out, "index.html"))
}

func write(path string, f func(*os.File) error) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f(file); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// series draws a run's queue, orbit and goodput over time, with the
// trigger shaded.
func series(f *os.File, r qload.Recovery) error {
	const W, H, pad = 640, 300, 40
	s := r.Metrics.Samples
	if len(s) == 0 {
		return nil
	}
	end := s[len(s)-1].T.Seconds()
	top := float64(retrystorm.QueueCap)
	for _, x := range s {
		top = max(top, float64(x.Orbit))
	}
	x := func(t float64) float64 { return pad + t/end*(W-2*pad) }
	y := func(v float64) float64 { return H - pad - v/top*(H-2*pad) }
	fmt.Fprintf(f, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="sans-serif" font-size="11">`+"\n", W, H)
	fmt.Fprintf(f, `<rect width="%d" height="%d" fill="white"/>`+"\n", W, H)
	t0, t1 := r.Trigger.Start.Seconds(), (r.Trigger.Start + r.Trigger.Duration).Seconds()
	fmt.Fprintf(f, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="#fde2c8"/>`+"\n", x(t0), pad, x(t1)-x(t0), H-2*pad)
	fmt.Fprintf(f, `<text x="%d" y="%d" text-anchor="middle">time (s), trigger shaded; recovered: %v</text>`+"\n", W/2, H-8, r.Recovered)
	fmt.Fprintf(f, `<text x="%d" y="%.0f" text-anchor="end">%.0f</text><text x="%d" y="%d" text-anchor="end">0</text>`+"\n", pad-4, y(top)+4, top, pad-4, H-pad+4)
	lines := []struct {
		name, color string
		v           func(qload.Sample) float64
	}{
		{"queue", "#1f77b4", func(x qload.Sample) float64 { return float64(x.Queue) }},
		{"orbit", "#d62728", func(x qload.Sample) float64 { return float64(x.Orbit) }},
		{"goodput/s ×5", "#2ca02c", func(x qload.Sample) float64 { return 5 * float64(x.Successes) }},
	}
	for i, l := range lines {
		var pts strings.Builder
		for k := 0; k < len(s); k += 5 {
			// a 5 s average smooths the per-second counts
			sum := 0.0
			for j := k; j < min(k+5, len(s)); j++ {
				sum += l.v(s[j])
			}
			fmt.Fprintf(&pts, "%.1f,%.1f ", x(s[k].T.Seconds()), y(sum/float64(min(5, len(s)-k))))
		}
		fmt.Fprintf(f, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`+"\n", pts.String(), l.color)
		fmt.Fprintf(f, `<text x="%d" y="%d" fill="%s">%s</text>`+"\n", pad+10+i*110, pad-8, l.color, l.name)
	}
	_, err := fmt.Fprintln(f, "</svg>")
	return err
}

// record writes the budget's steps over the Burst scenario.
func record(path, mutation string) error {
	m := map[string]retry.Mutation{"": retry.Faithful, "uncapped": retry.Uncapped, "deposit-on-retry": retry.DepositOnRetry}
	mut, ok := m[mutation]
	if !ok {
		return fmt.Errorf("unknown mutation %q: want uncapped or deposit-on-retry", mutation)
	}
	sink, err := qobs.FileSink(path)
	if err != nil {
		return err
	}
	qobs.SetGlobal(qobs.NewRecorder(sink, qobs.WithActor("client")))
	retrystorm.Burst(&retry.Budget{Ratio: retrystorm.BudgetRatio, Max: retrystorm.BudgetMax, Mutation: mut})
	qobs.SetGlobal(nil)
	return sink.Close()
}
