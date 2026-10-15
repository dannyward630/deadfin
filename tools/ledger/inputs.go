// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// --- Input types ---

// openAPIOperation is one operation from the pinned OpenAPI spec.
type openAPIOperation struct {
	OperationID string `json:"operationId"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Tag         string `json:"tag"`
}

// routeSnapshot is the generated deadfin route snapshot.
type routeSnapshot struct {
	Format   string          `json:"format"`
	Revision string          `json:"revision"`
	Routes   []snapshotRoute `json:"routes"`
}

type snapshotRoute struct {
	Method      string `json:"Method"`
	Pattern     string `json:"Pattern"`
	OperationID string `json:"OperationId"`
	Policy      string `json:"Policy,omitempty"`
	Anonymous   bool   `json:"Anonymous,omitempty"`
}

// resultFile is a difftool results JSON file.
type resultFile struct {
	Format string       `json:"format"`
	RunID  string       `json:"run_id"`
	Steps  []resultStep `json:"steps"`
}

type resultStep struct {
	Scenario    string `json:"scenario"`
	Step        string `json:"step"`
	OperationID string `json:"operation-id"`
	Status      string `json:"status"`
}

// shadowSpec is the hand-edited shadow-spec.yaml.
type shadowSpec struct {
	Operations []shadowOperation `yaml:"operations"`
}

type shadowOperation struct {
	ID        string   `yaml:"id"`
	Method    string   `yaml:"method"`
	Path      string   `yaml:"path"`
	Subsystem string   `yaml:"subsystem"`
	Upstream  string   `yaml:"upstream"`
	Clients   []string `yaml:"clients"`
	Swiftfin  bool     `yaml:"swiftfin,omitempty"`
}

// annotations is the hand-edited annotations.yaml.
type annotations struct {
	Source     sourceMeta              `yaml:"source"`
	Operations map[string]opAnnotation `yaml:"operations"`
	WebSocket  map[string]wsAnnotation `yaml:"websocket"`
}

type sourceMeta struct {
	DeadfinRevision string `yaml:"deadfin_revision"`
	JellyfinPin     string `yaml:"jellyfin_pin"`
}

type opAnnotation struct {
	Status      string   `yaml:"status"`
	Disposition string   `yaml:"disposition"`
	Reason      string   `yaml:"reason"`
	Ratchet     bool     `yaml:"ratchet,omitempty"`
	PinReset    bool     `yaml:"pin-reset,omitempty"`
	RecordedPin string   `yaml:"recorded_pin,omitempty"`
	Clients     []string `yaml:"clients,omitempty"`
}

type wsAnnotation struct {
	Status   string `yaml:"status"`
	Reason   string `yaml:"reason"`
	Evidence string `yaml:"evidence,omitempty"`
}

// wpFile is one work-package status line.
type wpFile struct {
	Path   string
	Phase  string
	Name   string
	Raw    string
	Status string
}

// --- Inputs container ---

type inputs struct {
	OpenAPI       []openAPIOperation
	Routes        []snapshotRoute
	RouteRevision string
	Results       map[string][]resultStep
	Shadow        shadowSpec
	Annotations   annotations
	WPFiles       []wpFile
	Root          string
}

// loadInputs reads and validates all committed inputs.
func loadInputs(root string) (*inputs, error) {
	in := &inputs{Root: root, Results: make(map[string][]resultStep)}

	if err := loadOpenAPI(root, in); err != nil {
		return nil, err
	}
	if err := loadRouteSnapshot(root, in); err != nil {
		return nil, err
	}
	if err := loadResults(root, in); err != nil {
		return nil, err
	}
	if err := loadShadowSpec(root, in); err != nil {
		return nil, err
	}
	if err := loadAnnotations(root, in); err != nil {
		return nil, err
	}
	if err := loadWPFiles(root, in); err != nil {
		return nil, err
	}

	return in, nil
}

func loadOpenAPI(root string, in *inputs) error {
	path := filepath.Join(root, "ledger", "inputs", "jellyfin-openapi-stable.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read openapi spec: %w", err)
	}

	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string   `json:"operationId"`
			Tags        []string `json:"tags"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parse openapi spec: %w", err)
	}

	for path, methods := range spec.Paths {
		for method, op := range methods {
			if op.OperationID == "" {
				continue
			}
			tag := ""
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			in.OpenAPI = append(in.OpenAPI, openAPIOperation{
				OperationID: op.OperationID,
				Method:      strings.ToUpper(method),
				Path:        path,
				Tag:         tag,
			})
		}
	}

	// Sort by operation ID for stable output.
	sortByOperationID(in.OpenAPI)
	return nil
}

func loadRouteSnapshot(root string, in *inputs) error {
	path := filepath.Join(root, "ledger", "deadfin-routes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read route snapshot: %w (run: make ledger-routes DEADFIN_DIR=<checkout> DEADFIN_REF=<revision>)", err)
	}

	var snap routeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("parse route snapshot: %w", err)
	}

	if snap.Format != routeSnapshotFormat {
		return fmt.Errorf("route snapshot has wrong format %q; expected %q", snap.Format, routeSnapshotFormat)
	}

	if len(snap.Revision) != 40 {
		return fmt.Errorf("route snapshot revision must be a 40-character SHA; got %q", snap.Revision)
	}

	in.Routes = snap.Routes
	in.RouteRevision = snap.Revision
	return nil
}

func loadResults(root string, in *inputs) error {
	dir := filepath.Join(root, "results")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read results directory: %w", err)
	}

	var runIDs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read result file %s: %w", entry.Name(), err)
		}

		var rf resultFile
		if err := json.Unmarshal(data, &rf); err != nil {
			return fmt.Errorf("parse result file %s: %w", entry.Name(), err)
		}

		if rf.Format != difftoolResultsFormat {
			continue
		}

		// Reject duplicate run_id values across files.
		for _, existing := range runIDs {
			if existing == rf.RunID {
				return fmt.Errorf("duplicate run_id %q in %s", rf.RunID, entry.Name())
			}
		}
		runIDs = append(runIDs, rf.RunID)

		in.Results[rf.RunID] = rf.Steps
	}

	return nil
}

func loadShadowSpec(root string, in *inputs) error {
	path := filepath.Join(root, "ledger", "shadow-spec.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read shadow-spec.yaml: %w", err)
	}

	if err := yaml.Unmarshal(data, &in.Shadow); err != nil {
		return fmt.Errorf("parse shadow-spec.yaml: %w", err)
	}

	return nil
}

func loadAnnotations(root string, in *inputs) error {
	path := filepath.Join(root, "ledger", "annotations.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read annotations.yaml: %w", err)
	}

	if err := yaml.Unmarshal(data, &in.Annotations); err != nil {
		return fmt.Errorf("parse annotations.yaml: %w", err)
	}

	return nil
}

func loadWPFiles(root string, in *inputs) error {
	dir := filepath.Join(root, "docs", "work-packages")

	// Parse INDEX.md to get the list of listed state files.
	indexPath := filepath.Join(dir, "INDEX.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("read INDEX.md: %w", err)
	}

	listedFiles := parseWPIndex(string(indexData))
	if len(listedFiles) == 0 {
		return fmt.Errorf("INDEX.md lists no work-package files")
	}

	// Parse each listed file.
	for _, name := range listedFiles {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read work package %s: %w (listed in INDEX.md)", name, err)
		}

		wf, err := parseWPFile(name, string(data))
		if err != nil {
			return fmt.Errorf("parse work package %s: %w", name, err)
		}

		in.WPFiles = append(in.WPFiles, wf)
	}

	return nil
}

// parseWPFile extracts the phase, name, and Status: line from a WP file.
// A Status: value can span multiple lines. Continuation lines are non-empty,
// non-heading lines that follow the Status: line before a blank line, a
// Markdown heading, or a Phase: key. The full source text is kept in Raw for
// traceability; normalizeStatus reads only the state prefix.
func parseWPFile(name string, content string) (wpFile, error) {
	wf := wpFile{Path: name, Name: strings.TrimSuffix(name, ".md")}

	// Extract phase from the filename: WP-P0-01 → P0, P5 → P5, etc.
	wf.Phase = extractPhase(name)

	// Find the Status: line and collect continuation lines.
	lines := strings.Split(content, "\n")
	statusIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "Status:") {
			statusIdx = i
			break
		}
	}
	if statusIdx == -1 {
		return wf, fmt.Errorf("no Status: line found in %s", name)
	}

	// Collect the Status: line and continuation lines.
	var rawLines []string
	rawLines = append(rawLines, strings.TrimPrefix(lines[statusIdx], "Status:"))
	for i := statusIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if strings.HasPrefix(line, "#") {
			break
		}
		if strings.HasPrefix(line, "Phase:") {
			break
		}
		rawLines = append(rawLines, line)
	}

	wf.Raw = strings.TrimSpace(strings.Join(rawLines, " "))
	status, err := normalizeStatus(wf.Raw)
	if err != nil {
		return wf, fmt.Errorf("%s: %w", name, err)
	}
	wf.Status = status

	return wf, nil
}

// parseWPIndex reads the INDEX.md markdown table and extracts the list of
// work-package filenames. The table has the format:
// | [WP-P0-01](WP-P0-01.md) | Title | P0 |
func parseWPIndex(content string) []string {
	var files []string
	for _, line := range strings.Split(content, "\n") {
		// Look for markdown links to .md files in the WP list table.
		// Format: | [WP-P0-01](WP-P0-01.md) | ... | or | [P5](P5.md) | ... |
		idx := strings.Index(line, "](")
		for idx >= 0 {
			rest := line[idx+2:]
			end := strings.Index(rest, ")")
			if end < 0 {
				break
			}
			link := rest[:end]
			if strings.HasSuffix(link, ".md") {
				// Only include WP-P* and P*.md files (not INDEX.md itself).
				base := filepath.Base(link)
				if base != "INDEX.md" && (strings.HasPrefix(base, "WP-P") || strings.HasPrefix(base, "P")) {
					files = append(files, base)
				}
			}
			line = rest[end+1:]
			idx = strings.Index(line, "](")
		}
	}
	return files
}

// extractPhase gets the phase prefix from a work-package filename.
// WP-P0-01.md → P0, P5.md → P5, WP-P5-01.md → P5.
func extractPhase(name string) string {
	// Match P followed by digits: P0, P1, ... P10
	for i := 0; i < len(name); i++ {
		if name[i] == 'P' && i+1 < len(name) && name[i+1] >= '0' && name[i+1] <= '9' {
			j := i + 1
			for j < len(name) && name[j] >= '0' && name[j] <= '9' {
				j++
			}
			return name[i:j]
		}
	}
	return ""
}

// normalizeStatus maps a raw Status: line to one of: todo, in-progress,
// done, blocked. It also accepts the two legacy forms: "complete" maps to
// done, and "implementation complete" with pending evidence maps to
// in-progress. It rejects "evidence-ready" and any other unknown state phrase.
//
// A valid state token must be followed by end-of-string or a space. The
// space is the word boundary that separates the state from an optional
// detail suffix such as "done — verified 2026-07-26". This rejects unknown
// prefixed phrases such as "doneish" or "completed" that would otherwise
// match a valid state by prefix.
func normalizeStatus(raw string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(raw))

	// Check for legacy "implementation complete" with pending evidence.
	if hasStateToken(lower, "implementation complete") {
		if strings.Contains(lower, "pending") || strings.Contains(lower, "remains") {
			return "in-progress", nil
		}
		return "done", nil
	}

	// Check for legacy "complete".
	if hasStateToken(lower, "complete") {
		return "done", nil
	}

	// Reject "evidence-ready" — it is not in the accepted vocabulary.
	if hasStateToken(lower, "evidence-ready") {
		return "", fmt.Errorf("unknown status phrase %q: evidence-ready is not an accepted state; use todo, in-progress, done, or blocked", raw)
	}

	// Standard state tokens.
	for _, status := range []string{"todo", "in-progress", "done", "blocked"} {
		if hasStateToken(lower, status) {
			return status, nil
		}
	}

	return "", fmt.Errorf("unknown status phrase %q: use todo, in-progress, done, or blocked", raw)
}

// hasStateToken reports whether lower starts with the state token state,
// followed by end-of-string or a space. The space is the word boundary
// that separates a valid state from an optional detail suffix. This
// rejects unknown prefixed phrases such as "doneish" or "completed" that
// would otherwise match a valid state by prefix.
func hasStateToken(lower, state string) bool {
	if !strings.HasPrefix(lower, state) {
		return false
	}
	return len(lower) == len(state) || lower[len(state)] == ' '
}

// exportRoutes runs the deadfin routes command and writes the snapshot.
func exportRoutes(root string, args []string) error {
	deadfinDir := os.Getenv("DEADFIN_DIR")
	deadfinRef := os.Getenv("DEADFIN_REF")

	if deadfinDir == "" {
		return errors.New("DEADFIN_DIR is required (set to the deadfin checkout path)")
	}
	if deadfinRef == "" {
		return errors.New("DEADFIN_REF is required (set to the 40-character deadfin revision)")
	}

	if len(deadfinRef) != 40 {
		return fmt.Errorf("DEADFIN_REF must be a 40-character SHA; got %d characters", len(deadfinRef))
	}

	// Reject a dirty checkout.
	cmd := exec.Command("git", "-C", deadfinDir, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check deadfin checkout status: %w", err)
	}
	if len(output) > 0 {
		return fmt.Errorf("deadfin checkout is dirty; commit or stash changes before exporting routes")
	}

	// Reject a checkout whose HEAD differs from DEADFIN_REF.
	cmd = exec.Command("git", "-C", deadfinDir, "rev-parse", "HEAD")
	headOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("get deadfin HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headOutput))
	if head != deadfinRef {
		return fmt.Errorf("deadfin HEAD %q does not match DEADFIN_REF %q", head, deadfinRef)
	}

	// Execute the route export from the deadfin checkout.
	cmd = exec.Command("go", "run", "./cmd/deadfin", "routes", "--json")
	cmd.Dir = deadfinDir
	routesJSON, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("run deadfin routes --json: %w", err)
	}

	// Parse the routes.
	var routes []snapshotRoute
	if err := json.Unmarshal(routesJSON, &routes); err != nil {
		return fmt.Errorf("parse deadfin routes output: %w", err)
	}

	// Sort by operation ID.
	sortRoutesByOperationID(routes)

	// Build the snapshot.
	snap := routeSnapshot{
		Format:   routeSnapshotFormat,
		Revision: deadfinRef,
		Routes:   routes,
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal route snapshot: %w", err)
	}
	data = append(data, '\n')

	outPath := filepath.Join(root, "ledger", "deadfin-routes.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write route snapshot: %w", err)
	}

	fmt.Printf("wrote %s (%d routes at revision %s)\n", outPath, len(routes), deadfinRef)
	return nil
}

// sortByOperationID sorts operations by operation ID for stable output.
func sortByOperationID(ops []openAPIOperation) {
	for i := 1; i < len(ops); i++ {
		for j := i; j > 0 && ops[j-1].OperationID > ops[j].OperationID; j-- {
			ops[j], ops[j-1] = ops[j-1], ops[j]
		}
	}
}

// sortRoutesByOperationID sorts routes by operation ID for stable output.
func sortRoutesByOperationID(routes []snapshotRoute) {
	for i := 1; i < len(routes); i++ {
		for j := i; j > 0 && routes[j-1].OperationID > routes[j].OperationID; j-- {
			routes[j], routes[j-1] = routes[j-1], routes[j]
		}
	}
}
