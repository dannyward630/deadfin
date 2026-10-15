// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	playstateStockItemID         = "7e897749137a8161f7a084cfad246bd3"
	playstateSessionID           = "00000000-0000-0000-0000-000000000111"
	playstateUnrelatedUserID     = "00000000-0000-0000-0000-000000000222"
	playstateProgressTicks   int = 100000
)

var playstateItemNormalizers = []string{
	"item-id-map",
	"server-id",
	"entity-tags",
	"item-created-dates",
	"user-data-keys",
	"item-user-data-playback-timestamps",
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-length-header",
	"content-language-header",
	"vary-header",
}

var playstatePostMutationItemNormalizers = []string{
	"item-id-map",
	"server-id",
	"entity-tags",
	"item-created-dates",
	"user-data-keys",
	"item-user-data-playback-timestamps",
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-length-header",
	"content-language-header",
	"vary-header",
}

var playstateResponseNormalizers = []string{
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-language-header",
}

var playstateBadRequestNormalizers = append(append([]string(nil), playstateResponseNormalizers...), "content-length-header")

type playstateCorpusStep struct {
	name        string
	method      string
	path        string
	operationID string
	params      map[string]any
	body        any
	normalizers []string
	status      int
}

func TestPlaystateCorpusDefinesExactStatefulSwiftfinContract(t *testing.T) {
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "playstate.yaml"))
	if err != nil {
		t.Fatalf("load committed playstate corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed playstate corpus scenarios = %d, want one", len(scenarios))
	}
	scenario := scenarios[0]
	if scenario.Format != scenarioFormat || scenario.Name != "stateful Swiftfin playstate parity" || scenario.BaseURLEnv != "JELLYFIN_BASE_URL" {
		t.Fatalf("playstate scenario metadata = %#v, want goldenrec scenario v1 with the fixed Jellyfin base URL", scenario)
	}

	contract := playstateCorpusContract()
	if len(scenario.Steps) != len(contract) {
		t.Fatalf("playstate corpus steps = %d, want %d", len(scenario.Steps), len(contract))
	}
	for index, expected := range contract {
		step := scenario.Steps[index]
		if step.Name != expected.name || step.Method != expected.method || step.Path != expected.path || step.OperationID != expected.operationID {
			t.Fatalf("playstate step %d = (%q, %s, %q, %q), want (%q, %s, %q, %q)", index+1, step.Name, step.Method, step.Path, step.OperationID, expected.name, expected.method, expected.path, expected.operationID)
		}
		if step.AuthRole != "admin" || step.AuthCarrier != "" || len(step.Headers) != 0 || len(step.Capture) != 0 {
			t.Fatalf("playstate step %q authentication = role %q carrier %q headers %#v capture %#v, want only the bootstrapped admin role", step.Name, step.AuthRole, step.AuthCarrier, step.Headers, step.Capture)
		}
		if !reflect.DeepEqual(step.Params, expected.params) || !reflect.DeepEqual(step.Body, expected.body) {
			t.Fatalf("playstate step %q request = params %#v body %#v, want params %#v body %#v", step.Name, step.Params, step.Body, expected.params, expected.body)
		}
		if step.BodyFile != "" || step.BodyFileField != "" || len(step.Normalize) != 0 || !reflect.DeepEqual(step.Normalizers, expected.normalizers) {
			t.Fatalf("playstate step %q transport = body file %q/%q normalize %#v normalizers %#v, want inline request and explicit normalizers %#v", step.Name, step.BodyFile, step.BodyFileField, step.Normalize, step.Normalizers, expected.normalizers)
		}
		if step.Expect == nil || step.Expect.Status == nil || *step.Expect.Status != expected.status {
			t.Fatalf("playstate step %q expect.status = %#v, want %d", step.Name, step.Expect, expected.status)
		}
	}

	if err := requirePlaystateRouteCoverage(scenario.Steps); err != nil {
		t.Fatal(err)
	}
	if err := requirePlaystateHostLocalItemRewriting(scenario.Steps); err != nil {
		t.Fatal(err)
	}
}

func playstateCorpusContract() []playstateCorpusStep {
	itemPath := "/Items/" + playstateStockItemID
	legacyPath := "/PlayingItems/" + playstateStockItemID
	legacyUserPath := "/Users/" + playstateUnrelatedUserID + legacyPath
	return []playstateCorpusStep{
		playstateItemRead("baseline item read", itemPath),
		{
			name: "modern Swiftfin start", method: http.MethodPost, path: "/Sessions/Playing", operationID: "ReportPlaybackStart", body: playstateStartBody(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "modern Swiftfin ping", method: http.MethodPost, path: "/Sessions/Playing/Ping", operationID: "PingPlaybackSession", params: map[string]any{"playSessionId": playstateSessionID}, normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "modern Swiftfin progress below resume threshold", method: http.MethodPost, path: "/Sessions/Playing/Progress", operationID: "ReportPlaybackProgress", body: playstateProgressBody(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		playstatePostMutationItemRead("item read after modern progress", itemPath),
		{
			name: "modern Swiftfin failed stop", method: http.MethodPost, path: "/Sessions/Playing/Stopped", operationID: "ReportPlaybackStopped", body: playstateStopBody(5000000, true), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		playstatePostMutationItemRead("item read after failed modern stop", itemPath),
		{
			name: "modern Swiftfin restart", method: http.MethodPost, path: "/Sessions/Playing", operationID: "ReportPlaybackStart", body: playstateStartBody(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "modern Swiftfin negative stop", method: http.MethodPost, path: "/Sessions/Playing/Stopped", operationID: "ReportPlaybackStopped", body: playstateStopBody(-1, false), normalizers: playstateBadRequestNormalizers, status: http.StatusBadRequest,
		},
		{
			name: "modern Swiftfin valid short-runtime stop", method: http.MethodPost, path: "/Sessions/Playing/Stopped", operationID: "ReportPlaybackStopped", body: playstateStopBody(600000, false), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		playstatePostMutationItemRead("item read after modern stop", itemPath),
		{
			name: "legacy start without user prefix", method: http.MethodPost, path: legacyPath, operationID: "SHADOW:OnPlaybackStart", params: playstateLegacyStartParams(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "legacy progress without user prefix", method: http.MethodPost, path: legacyPath + "/Progress", operationID: "SHADOW:OnPlaybackProgress", params: playstateLegacyProgressParams(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "legacy stop without user prefix", method: http.MethodDelete, path: legacyPath, operationID: "SHADOW:OnPlaybackStopped", params: playstateLegacyStopParams(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		playstatePostMutationItemRead("item read after legacy stop", itemPath),
		{
			name: "legacy start with unrelated user prefix", method: http.MethodPost, path: legacyUserPath, operationID: "SHADOW:OnPlaybackStartLegacy", params: playstateLegacyStartParams(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "legacy progress with unrelated user prefix", method: http.MethodPost, path: legacyUserPath + "/Progress", operationID: "SHADOW:OnPlaybackProgressLegacy", params: playstateLegacyProgressParams(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		{
			name: "legacy stop with unrelated user prefix", method: http.MethodDelete, path: legacyUserPath, operationID: "SHADOW:OnPlaybackStoppedLegacy", params: playstateLegacyStopParams(), normalizers: playstateResponseNormalizers, status: http.StatusNoContent,
		},
		playstatePostMutationItemRead("final item read", itemPath),
	}
}

func playstateItemRead(name, path string) playstateCorpusStep {
	return playstateCorpusStep{name: name, method: http.MethodGet, path: path, operationID: "GetItem", params: map[string]any{"Fields": []any{"Height", "Width", "Path", "PrimaryImageAspectRatio", "MediaSources", "MediaStreams"}}, normalizers: playstateItemNormalizers, status: http.StatusOK}
}

func playstatePostMutationItemRead(name, path string) playstateCorpusStep {
	return playstateCorpusStep{name: name, method: http.MethodGet, path: path, operationID: "GetItem", params: map[string]any{"Fields": []any{"Height", "Width", "Path", "PrimaryImageAspectRatio", "MediaSources", "MediaStreams"}}, normalizers: playstatePostMutationItemNormalizers, status: http.StatusOK}
}

func playstateStartBody() map[string]any {
	return map[string]any{
		"CanSeek":       true,
		"ItemId":        playstateStockItemID,
		"MediaSourceId": playstateStockItemID,
		"PlayMethod":    "DirectPlay",
		"PlaySessionId": playstateSessionID,
		"PositionTicks": 0,
		"SessionId":     playstateSessionID,
	}
}

func playstateProgressBody() map[string]any {
	return map[string]any{
		"AudioStreamIndex":    0,
		"CanSeek":             true,
		"IsMuted":             false,
		"IsPaused":            false,
		"ItemId":              playstateStockItemID,
		"MediaSourceId":       playstateStockItemID,
		"PlayMethod":          "DirectPlay",
		"PlaySessionId":       playstateSessionID,
		"PositionTicks":       playstateProgressTicks,
		"RepeatMode":          "RepeatNone",
		"SessionId":           playstateSessionID,
		"SubtitleStreamIndex": -1,
		"VolumeLevel":         100,
	}
}

func playstateStopBody(position int, failed bool) map[string]any {
	return map[string]any{
		"Failed":        failed,
		"ItemId":        playstateStockItemID,
		"MediaSourceId": playstateStockItemID,
		"PlaySessionId": playstateSessionID,
		"PositionTicks": position,
		"SessionId":     playstateSessionID,
	}
}

func playstateLegacyStartParams() map[string]any {
	return map[string]any{
		"audioStreamIndex":    0,
		"canSeek":             true,
		"liveStreamId":        "",
		"mediaSourceId":       playstateStockItemID,
		"playMethod":          "DirectPlay",
		"playSessionId":       playstateSessionID,
		"subtitleStreamIndex": -1,
	}
}

func playstateLegacyProgressParams() map[string]any {
	return map[string]any{
		"audioStreamIndex":    0,
		"canSeek":             true,
		"isMuted":             false,
		"isPaused":            false,
		"liveStreamId":        "",
		"mediaSourceId":       playstateStockItemID,
		"playMethod":          "DirectPlay",
		"playSessionId":       playstateSessionID,
		"positionTicks":       playstateProgressTicks,
		"repeatMode":          "RepeatNone",
		"subtitleStreamIndex": -1,
		"volumeLevel":         100,
	}
}

func playstateLegacyStopParams() map[string]any {
	return map[string]any{
		"liveStreamId":  "",
		"mediaSourceId": playstateStockItemID,
		"playSessionId": playstateSessionID,
	}
}

func requirePlaystateRouteCoverage(steps []Step) error {
	want := map[string]int{
		"ReportPlaybackStart":             2,
		"PingPlaybackSession":             1,
		"ReportPlaybackProgress":          1,
		"ReportPlaybackStopped":           3,
		"SHADOW:OnPlaybackStart":          1,
		"SHADOW:OnPlaybackProgress":       1,
		"SHADOW:OnPlaybackStopped":        1,
		"SHADOW:OnPlaybackStartLegacy":    1,
		"SHADOW:OnPlaybackProgressLegacy": 1,
		"SHADOW:OnPlaybackStoppedLegacy":  1,
	}
	got := make(map[string]int, len(want))
	for _, step := range steps {
		if _, tracked := want[step.OperationID]; tracked {
			got[step.OperationID]++
		}
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("playstate route coverage = %#v, want every modern and legacy operation %#v", got, want)
	}
	return nil
}

func requirePlaystateHostLocalItemRewriting(steps []Step) error {
	host := &httpHost{
		sourceIDs:      map[string]string{playstateStockItemID: "fixture-item"},
		destinationIDs: map[string]string{"fixture-item": "host-local-item"},
	}
	for _, authored := range steps {
		authoredCount := strings.Count(authored.Path, playstateStockItemID) + countPlaystateItemID(authored.Params, playstateStockItemID) + countPlaystateItemID(authored.Body, playstateStockItemID)
		if authoredCount == 0 {
			continue
		}
		rewritten, err := host.rewriteStep(authored)
		if err != nil {
			return fmt.Errorf("rewrite playstate step %q: %w", authored.Name, err)
		}
		rewrittenCount := strings.Count(rewritten.Path, "host-local-item") + countPlaystateItemID(rewritten.Params, "host-local-item") + countPlaystateItemID(rewritten.Body, "host-local-item")
		if rewrittenCount != authoredCount || strings.Contains(rewritten.Path, playstateStockItemID) || countPlaystateItemID(rewritten.Params, playstateStockItemID) != 0 || countPlaystateItemID(rewritten.Body, playstateStockItemID) != 0 {
			return fmt.Errorf("playstate step %q rewrote %d of %d stock item literals host-locally: %#v", authored.Name, rewrittenCount, authoredCount, rewritten)
		}
	}
	return nil
}

func countPlaystateItemID(value any, itemID string) int {
	switch value := value.(type) {
	case string:
		return strings.Count(value, itemID)
	case map[string]any:
		count := 0
		for _, entry := range value {
			count += countPlaystateItemID(entry, itemID)
		}
		return count
	}
	return 0
}
