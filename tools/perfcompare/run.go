// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// schemaVersion changes when the JSON result shape changes.
const schemaVersion = 1

// Run is one comparison. One Run is one JSONL line.
type Run struct {
	Schema       int              `json:"schema"`
	StartedAt    time.Time        `json:"startedAt"`
	FinishedAt   time.Time        `json:"finishedAt"`
	Iterations   int              `json:"iterations"`
	Warmup       int              `json:"warmup"`
	Jellyfin     ServerInfo       `json:"jellyfin"`
	Deadfin      ServerInfo       `json:"deadfin"`
	Noisy        bool             `json:"noisy"`
	NoisyReasons []string         `json:"noisyReasons,omitempty"`
	Mapping      []mapEntry       `json:"mapping"`
	Scenarios    []ScenarioResult `json:"scenarios"`
	Warnings     []string         `json:"warnings,omitempty"`
}

// ServerInfo identifies one server. Host is the operator's note on where
// the pod runs; the API does not expose it.
type ServerInfo struct {
	URL        string          `json:"url"`
	Host       string          `json:"host,omitempty"`
	Version    string          `json:"version"`
	ServerName string          `json:"serverName"`
	Build      json.RawMessage `json:"build,omitempty"`
	TasksStart []string        `json:"runningTasksAtStart,omitempty"`
	TasksEnd   []string        `json:"runningTasksAtEnd,omitempty"`
	TasksError string          `json:"tasksError,omitempty"`
}

// ScenarioResult compares one scenario across the servers.
type ScenarioResult struct {
	Name     string `json:"name"`
	Jellyfin *Side  `json:"jellyfin,omitempty"`
	Deadfin  *Side  `json:"deadfin,omitempty"`
	// RatioP50 and RatioP95 are Deadfin over Jellyfin. Above 1 is slower.
	RatioP50 float64 `json:"ratioP50"`
	RatioP95 float64 `json:"ratioP95"`
	// Diff names a correctness difference: status, count, total, or sig.
	Diff string `json:"diff,omitempty"`
}

// Side is one server's result for one scenario.
type Side struct {
	latency
	Status int    `json:"status"`
	Errors int    `json:"errors"`
	Bytes  int64  `json:"bytes"`
	Shape  *shape `json:"shape,omitempty"`
	Error  string `json:"error,omitempty"`
	// Unmapped says why the scenario sent no request: discovery found no
	// id on this server for an item the other server has.
	Unmapped string `json:"unmapped,omitempty"`
}

type options struct {
	iterations, warmup, hlsRuns int
	user                        string
	jellyfinHost, deadfinHost   string
	now                         func() time.Time
}

// collector gathers samples for one scenario on one server.
type collector struct {
	samples  []time.Duration
	status   int
	errors   int
	bytes    int64
	shape    *shape
	err      string
	unmapped string
}

func (c *collector) side() *Side {
	if c == nil {
		return nil
	}
	return &Side{latency: summarize(c.samples), Status: c.status, Errors: c.errors, Bytes: c.bytes, Shape: c.shape, Error: c.err, Unmapped: c.unmapped}
}

// compare runs discovery, the scenario set, and the HLS starts.
func compare(ctx context.Context, jf, df *server, opt options) (Run, error) {
	run := Run{Schema: schemaVersion, StartedAt: opt.now().UTC(), Iterations: opt.iterations, Warmup: opt.warmup}
	run.Jellyfin = serverInfo(ctx, jf, opt.jellyfinHost)
	run.Deadfin = serverInfo(ctx, df, opt.deadfinHost)
	run.Jellyfin.TasksStart, run.Jellyfin.TasksError = runningTasks(ctx, jf)
	run.Deadfin.TasksStart, run.Deadfin.TasksError = runningTasks(ctx, df)

	jt, dt, mapping, warnings, err := discover(ctx, jf, df, opt.user)
	if err != nil {
		return run, fmt.Errorf("discover: %w", err)
	}
	run.Mapping, run.Warnings = mapping, warnings

	now := opt.now()
	jsc, dsc := buildScenarios(jt, now), buildScenarios(dt, now)
	names, jByName, dByName := alignScenarios(jsc, dsc)

	jc := map[string]*collector{}
	dc := map[string]*collector{}
	for _, n := range names {
		if _, ok := jByName[n]; ok {
			jc[n] = &collector{}
		}
		if _, ok := dByName[n]; ok {
			dc[n] = &collector{}
		}
	}

	total := opt.warmup + opt.iterations
	for i := 0; i < total; i++ {
		if err := ctx.Err(); err != nil {
			return run, err
		}
		record := i >= opt.warmup
		for _, n := range names {
			// Alternate which server goes first, so that neither always
			// meets a cold cache or a busy shared NFS mount.
			order := []func(){
				func() { measure(ctx, jf, jByName[n], jc[n], record) },
				func() { measure(ctx, df, dByName[n], dc[n], record) },
			}
			if i%2 == 1 {
				order[0], order[1] = order[1], order[0]
			}
			for _, f := range order {
				f()
			}
		}
	}

	for _, n := range names {
		run.Scenarios = append(run.Scenarios, result(n, jc[n].side(), dc[n].side()))
	}

	if opt.hlsRuns > 0 {
		run.Scenarios = append(run.Scenarios, hlsScenarios(ctx, jf, df, jt, dt, opt.hlsRuns, &run.Warnings)...)
	}

	run.Jellyfin.TasksEnd, _ = runningTasks(ctx, jf)
	run.Deadfin.TasksEnd, _ = runningTasks(ctx, df)
	run.Noisy, run.NoisyReasons = noisy(run.Jellyfin, run.Deadfin)
	run.FinishedAt = opt.now().UTC()
	return run, nil
}

// alignScenarios returns scenario names in Jellyfin order, then any names
// only Deadfin has, with each server's scenario by name.
func alignScenarios(j, d []scenario) ([]string, map[string]scenario, map[string]scenario) {
	jm, dm := map[string]scenario{}, map[string]scenario{}
	var names []string
	for _, s := range j {
		jm[s.Name] = s
		names = append(names, s.Name)
	}
	for _, s := range d {
		dm[s.Name] = s
		if _, ok := jm[s.Name]; !ok {
			names = append(names, s.Name)
		}
	}
	return names, jm, dm
}

// measure runs one scenario once and records the sample when record is set.
func measure(ctx context.Context, s *server, sc scenario, c *collector, record bool) {
	if c == nil {
		return
	}
	if sc.Unmapped != "" {
		c.unmapped = sc.Unmapped
		return
	}
	resp, err := s.do(ctx, sc.Method, sc.Path, sc.Body, !sc.Image)
	if !record {
		return
	}
	if err != nil {
		c.errors++
		c.err = err.Error()
		return
	}
	c.status = resp.Status
	c.bytes = resp.Bytes
	if resp.Status != http.StatusOK && resp.Status != http.StatusNoContent {
		c.errors++
		c.err = fmt.Sprintf("status %d", resp.Status)
		return
	}
	c.samples = append(c.samples, resp.Total)
	if !sc.Image && c.shape == nil {
		if sh, err := shapeOf(resp.Body); err == nil {
			c.shape = &sh
		}
	}
}

// result builds the comparison row and the correctness flag.
func result(name string, j, d *Side) ScenarioResult {
	r := ScenarioResult{Name: name, Jellyfin: j, Deadfin: d}
	switch {
	case j != nil && j.Unmapped != "":
		r.Diff = "unmapped on jellyfin"
		return r
	case d != nil && d.Unmapped != "":
		r.Diff = "unmapped on deadfin"
		return r
	case j == nil:
		r.Diff = "missing on jellyfin"
		return r
	case d == nil:
		r.Diff = "missing on deadfin"
		return r
	}
	r.RatioP50 = ratio(d.P50, j.P50)
	r.RatioP95 = ratio(d.P95, j.P95)
	r.Diff = diff(j, d)
	return r
}

// diff names the first correctness difference between two sides.
func diff(j, d *Side) string {
	if j.Status != d.Status {
		return fmt.Sprintf("status %d/%d", j.Status, d.Status)
	}
	if j.Shape == nil || d.Shape == nil {
		return ""
	}
	if j.Shape.Count != d.Shape.Count {
		return fmt.Sprintf("count %d/%d", j.Shape.Count, d.Shape.Count)
	}
	if j.Shape.Total != d.Shape.Total {
		return fmt.Sprintf("total %d/%d", j.Shape.Total, d.Shape.Total)
	}
	if j.Shape.Sig != d.Shape.Sig {
		return fmt.Sprintf("sig %s/%s", j.Shape.Sig, d.Shape.Sig)
	}
	return ""
}

// hlsScenarios runs the transcode starts. They are not warmed up: each
// start is a fresh ffmpeg job, and that cold start is what a player waits on.
func hlsScenarios(ctx context.Context, jf, df *server, jt, dt target, runs int, warnings *[]string) []ScenarioResult {
	names := []string{"hls/playback-info", "hls/master-ttfb", "hls/variant-ttfb", "hls/first-segment-ttfb"}
	type acc struct {
		samples  [4][]time.Duration
		bytes    int64
		errs     int
		err      string
		unmapped string
	}
	one := func(s *server, t target, a *acc) {
		if t.TranscodeMovie.ID == "" {
			if r := t.Unmapped["transcode-movie"]; r != "" {
				a.unmapped = r
				return
			}
			a.errs++
			a.err = "no transcode item"
			return
		}
		res, err := runHLS(ctx, s, t.UserID, t.TranscodeMovie.ID)
		if !res.Stopped && res.Info > 0 {
			*warnings = append(*warnings, s.label+": transcode stop did not confirm; the server reaps idle jobs")
		}
		if err != nil {
			a.errs++
			a.err = err.Error()
			return
		}
		for i, v := range []time.Duration{res.Info, res.Master, res.Variant, res.Segment} {
			if v > 0 {
				a.samples[i] = append(a.samples[i], v)
			}
		}
		a.bytes = res.SegmentBytes
	}
	var ja, da acc
	for i := 0; i < runs; i++ {
		if i%2 == 0 {
			one(jf, jt, &ja)
			one(df, dt, &da)
		} else {
			one(df, dt, &da)
			one(jf, jt, &ja)
		}
	}
	side := func(a acc, i int) *Side {
		s := &Side{latency: summarize(a.samples[i]), Errors: a.errs, Error: a.err, Unmapped: a.unmapped}
		if len(a.samples[i]) > 0 {
			s.Status = http.StatusOK
		}
		if i == 3 {
			s.Bytes = a.bytes
		}
		return s
	}
	var out []ScenarioResult
	for i, n := range names {
		out = append(out, result(n, side(ja, i), side(da, i)))
	}
	return out
}

func serverInfo(ctx context.Context, s *server, host string) ServerInfo {
	info := ServerInfo{URL: s.base, Host: host}
	var pub struct {
		Version    string `json:"Version"`
		ServerName string `json:"ServerName"`
	}
	if err := s.getJSON(ctx, "/System/Info/Public", &pub); err == nil {
		info.Version, info.ServerName = pub.Version, pub.ServerName
	}
	if resp, err := s.do(ctx, http.MethodGet, "/deadfin/v1/build", nil, true); err == nil && resp.Status == http.StatusOK && json.Valid(resp.Body) {
		info.Build = json.RawMessage(resp.Body)
	}
	return info
}

// runningTasks lists scheduled tasks in the Running state. A library scan
// or another heavy task during the run makes the latencies noisy.
func runningTasks(ctx context.Context, s *server) ([]string, string) {
	var tasks []struct {
		Name     string   `json:"Name"`
		Key      string   `json:"Key"`
		State    string   `json:"State"`
		Progress *float64 `json:"CurrentProgressPercentage"`
	}
	if err := s.getJSON(ctx, "/ScheduledTasks?isHidden=false", &tasks); err != nil {
		return nil, err.Error()
	}
	var running []string
	for _, t := range tasks {
		if t.State == "Running" {
			name := t.Name
			if t.Progress != nil {
				name = fmt.Sprintf("%s (%.0f%%)", t.Name, *t.Progress)
			}
			running = append(running, name)
		}
	}
	return running, ""
}

// noisy reports whether a task ran on either server at the start or end of
// the run, or whether task state was unreadable.
func noisy(j, d ServerInfo) (bool, []string) {
	var reasons []string
	for _, s := range []struct {
		label string
		info  ServerInfo
	}{{"jellyfin", j}, {"deadfin", d}} {
		for _, t := range s.info.TasksStart {
			reasons = append(reasons, s.label+" running at start: "+t)
		}
		for _, t := range s.info.TasksEnd {
			reasons = append(reasons, s.label+" running at end: "+t)
		}
		if s.info.TasksError != "" {
			reasons = append(reasons, s.label+" task state unknown: "+s.info.TasksError)
		}
	}
	return len(reasons) > 0, reasons
}
