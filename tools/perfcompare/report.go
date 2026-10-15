// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strings"
)

// hardwareNote is printed in every report header. The two pods do not run
// on the same hardware, so a ratio near 1 is not proof of parity and a
// ratio far from 1 is partly the CPU.
const hardwareNote = "Hardware differs: the servers run on different nodes and CPUs. " +
	"For a fair comparison pin Deadfin to the same node class as Jellyfin " +
	"with a nodeSelector or node affinity."

// writeTable writes the readable report as a Markdown table, which reads
// the same in a terminal and in a pull request.
func writeTable(w io.Writer, run Run) {
	fmt.Fprintf(w, "## perfcompare %s\n\n", run.StartedAt.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(w, "- Jellyfin %s (%s) at %s, host: %s\n", run.Jellyfin.Version, run.Jellyfin.ServerName, run.Jellyfin.URL, orUnknown(run.Jellyfin.Host))
	fmt.Fprintf(w, "- Deadfin %s (%s) at %s, host: %s\n", buildVersion(run.Deadfin), run.Deadfin.ServerName, run.Deadfin.URL, orUnknown(run.Deadfin.Host))
	fmt.Fprintf(w, "- %d iterations after %d warm-up; latency is request to last byte, ms; ratio is Deadfin/Jellyfin (>1 is slower)\n", run.Iterations, run.Warmup)
	if probes := probeNames(run.Mapping); probes != "" {
		fmt.Fprintf(w, "- probes: %s\n", probes)
	}
	fmt.Fprintf(w, "- %s\n", hardwareNote)
	if run.Noisy {
		fmt.Fprintf(w, "- **NOISY RUN**: %s\n", strings.Join(run.NoisyReasons, "; "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| scenario | JF p50 | JF p95 | JF max | DF p50 | DF p95 | DF max | p50 ratio | JF bytes | DF bytes | JF n | DF n | diff |")
	fmt.Fprintln(w, "|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|")
	for _, s := range run.Scenarios {
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			s.Name,
			ms(s.Jellyfin, func(x *Side) float64 { return x.P50 }),
			ms(s.Jellyfin, func(x *Side) float64 { return x.P95 }),
			ms(s.Jellyfin, func(x *Side) float64 { return x.Max }),
			ms(s.Deadfin, func(x *Side) float64 { return x.P50 }),
			ms(s.Deadfin, func(x *Side) float64 { return x.P95 }),
			ms(s.Deadfin, func(x *Side) float64 { return x.Max }),
			ratioCell(s.RatioP50),
			bytesCell(s.Jellyfin), bytesCell(s.Deadfin),
			countCell(s.Jellyfin), countCell(s.Deadfin),
			diffCell(s),
		)
	}
	if len(run.Warnings) > 0 {
		fmt.Fprintln(w)
		for _, warn := range run.Warnings {
			fmt.Fprintf(w, "- warning: %s\n", warn)
		}
	}
	var unmapped []string
	for _, m := range run.Mapping {
		if m.Method == "unmapped" {
			name := fmt.Sprintf("%s %q", m.Entity, m.Name)
			if m.Library != "" {
				name += fmt.Sprintf(" in %q", m.Library)
			}
			unmapped = append(unmapped, name)
		}
	}
	fmt.Fprintf(w, "\nid mapping: %d entities, %d unmapped\n", len(run.Mapping), len(unmapped))
	for _, u := range unmapped {
		fmt.Fprintf(w, "- unmapped: %s\n", u)
	}
}

// probeNames lists the probe items, so that a failed scenario names the
// item it used: entity "name" (library, how it mapped).
func probeNames(mapping []mapEntry) string {
	var parts []string
	for _, m := range mapping {
		if m.Entity == "library" {
			continue
		}
		where := m.Method
		if m.Library != "" {
			where = m.Library + ", " + where
		}
		parts = append(parts, fmt.Sprintf("%s %q (%s)", m.Entity, m.Name, where))
	}
	return strings.Join(parts, "; ")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// buildVersion prefers the native build identity over the compatibility
// version that /System/Info/Public reports.
func buildVersion(info ServerInfo) string {
	var b struct {
		Version  string `json:"Version"`
		Revision string `json:"Revision"`
	}
	if len(info.Build) > 0 && json.Unmarshal(info.Build, &b) == nil && b.Version != "" {
		rev := b.Revision
		if len(rev) > 8 {
			rev = rev[:8]
		}
		return fmt.Sprintf("%s@%s (compat %s)", b.Version, rev, info.Version)
	}
	return info.Version
}

func ms(s *Side, f func(*Side) float64) string {
	if s == nil || s.Samples == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", f(s))
}

func ratioCell(r float64) string {
	if r == 0 {
		return "-"
	}
	if r >= 2 {
		return fmt.Sprintf("**%.2f**", r)
	}
	return fmt.Sprintf("%.2f", r)
}

func bytesCell(s *Side) string {
	if s == nil || s.Bytes == 0 {
		return "-"
	}
	switch {
	case s.Bytes >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(s.Bytes)/(1<<20))
	case s.Bytes >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(s.Bytes)/(1<<10))
	}
	return fmt.Sprint(s.Bytes)
}

func countCell(s *Side) string {
	if s == nil || s.Shape == nil {
		return "-"
	}
	if s.Shape.Total >= 0 && s.Shape.Total != s.Shape.Count {
		return fmt.Sprintf("%d/%d", s.Shape.Count, s.Shape.Total)
	}
	return fmt.Sprint(s.Shape.Count)
}

func diffCell(s ScenarioResult) string {
	var parts []string
	if s.Diff != "" {
		parts = append(parts, "**"+s.Diff+"**")
	}
	for _, side := range []struct {
		label string
		s     *Side
	}{{"JF", s.Jellyfin}, {"DF", s.Deadfin}} {
		if side.s != nil && side.s.Errors > 0 {
			parts = append(parts, fmt.Sprintf("%s %d err: %s", side.label, side.s.Errors, side.s.Error))
		}
		if side.s != nil && side.s.Unmapped != "" {
			parts = append(parts, side.label+" "+side.s.Unmapped)
		}
	}
	return strings.ReplaceAll(strings.Join(parts, "; "), "|", "/")
}

// readRuns reads a JSONL history. It skips lines that do not decode, so
// that one bad append does not hide the rest.
func readRuns(r io.Reader) ([]Run, error) {
	var runs []Run
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var run Run
		if json.Unmarshal([]byte(line), &run) == nil {
			runs = append(runs, run)
		}
	}
	return runs, sc.Err()
}

// historyRow is one scenario's p50 ratio over the recent runs.
type historyRow struct {
	Name   string
	Ratios []historyCell
}

type historyCell struct {
	Ratio float64
	Diff  bool
	Noisy bool
}

// history builds the trend table: the last limit runs as columns, oldest
// first, and the scenarios of the newest run as rows.
func history(runs []Run, limit int) ([]Run, []historyRow) {
	if len(runs) > limit {
		runs = runs[len(runs)-limit:]
	}
	if len(runs) == 0 {
		return nil, nil
	}
	latest := runs[len(runs)-1]
	var rows []historyRow
	for _, s := range latest.Scenarios {
		row := historyRow{Name: s.Name}
		for _, run := range runs {
			cell := historyCell{Noisy: run.Noisy}
			for _, rs := range run.Scenarios {
				if rs.Name == s.Name {
					cell.Ratio, cell.Diff = rs.RatioP50, rs.Diff != ""
				}
			}
			row.Ratios = append(row.Ratios, cell)
		}
		rows = append(rows, row)
	}
	return runs, rows
}

// writeHTML writes the static report: the newest run in full and the p50
// ratio trend over the recent runs.
func writeHTML(w io.Writer, runs []Run, limit int) error {
	recent, rows := history(runs, limit)
	if len(recent) == 0 {
		return fmt.Errorf("no runs")
	}
	var table strings.Builder
	writeTable(&table, recent[len(recent)-1])
	return reportTemplate.Execute(w, map[string]any{
		"Runs":   recent,
		"Rows":   rows,
		"Latest": table.String(),
	})
}

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"cellClass": func(c historyCell) string {
		switch {
		case c.Ratio == 0:
			return "none"
		case c.Ratio >= 2:
			return "bad"
		case c.Ratio > 1.2:
			return "warn"
		case c.Ratio < 0.8:
			return "good"
		}
		return "even"
	},
	"short": func(r Run) string { return r.StartedAt.Format("01-02") },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Deadfin perfcompare</title>
<style>
:root{--bg:#fff;--fg:#1b1b1b;--muted:#666;--line:#ddd;--good:#d9f2dc;--even:#f4f4f4;--warn:#fbeccb;--bad:#f7d4d0}
@media (prefers-color-scheme:dark){:root{--bg:#141414;--fg:#e8e8e8;--muted:#999;--line:#333;--good:#1f3d24;--even:#222;--warn:#4a3b14;--bad:#4d1f1b}}
body{background:var(--bg);color:var(--fg);font:14px/1.4 system-ui,sans-serif;margin:0 auto;max-width:1200px;padding:16px}
table{border-collapse:collapse;font-variant-numeric:tabular-nums}td,th{border:1px solid var(--line);padding:2px 6px;text-align:right}
td:first-child,th:first-child{text-align:left}.good{background:var(--good)}.even{background:var(--even)}.warn{background:var(--warn)}.bad{background:var(--bad)}
.diff{outline:2px solid #c33;outline-offset:-2px}.noisy{opacity:.55}pre{white-space:pre-wrap;overflow-x:auto}.wrap{overflow-x:auto}
</style></head><body>
<h1>Deadfin vs Jellyfin</h1>
<p>p50 ratio Deadfin/Jellyfin per scenario, newest run on the right. Above 1 is slower. A red outline marks a result-count or status difference; faded columns are noisy runs.</p>
<div class="wrap"><table><tr><th>scenario</th>{{range .Runs}}<th class="{{if .Noisy}}noisy{{end}}" title="{{.StartedAt}}">{{short .}}</th>{{end}}</tr>
{{range .Rows}}<tr><td>{{.Name}}</td>{{range .Ratios}}<td class="{{cellClass .}}{{if .Diff}} diff{{end}}{{if .Noisy}} noisy{{end}}">{{if .Ratio}}{{printf "%.2f" .Ratio}}{{else}}-{{end}}</td>{{end}}</tr>
{{end}}</table></div>
<h2>Latest run</h2>
<pre>{{.Latest}}</pre>
</body></html>
`))
