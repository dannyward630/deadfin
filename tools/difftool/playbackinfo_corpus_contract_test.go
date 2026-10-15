// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
)

const subtitleSamplePlaybackInfoID = "ad08623a9ec58acc2d5541518d90a065"

var subtitlePlaybackInfoNormalizers = []string{
	"item-id-map",
	"entity-tags",
	"play-session-ids",
	"transcoding-url-tokens",
	"subtitle-delivery-url-item-ids",
	"response-time-headers",
	"response-time-ms-header",
	"process-time-headers",
	"date-header",
	"server-stack-header",
	"content-length-header",
	"content-language-header",
	"vary-header",
}

func TestPlaybackInfoCorpusSubtitleSampleComparesGETAndDeviceProfilePOST(t *testing.T) {
	get, post := subtitlePlaybackInfoSteps(t)

	for _, step := range []Step{get, post} {
		if len(step.Normalize) != 0 || !reflect.DeepEqual(step.Normalizers, subtitlePlaybackInfoNormalizers) {
			t.Fatalf("%s normalizers = %#v / %#v, want explicit subtitle normalizers %#v", step.Name, step.Normalize, step.Normalizers, subtitlePlaybackInfoNormalizers)
		}
	}
}

func TestPlaybackInfoCorpusSubtitleSampleRejectsWrongFixtureOrProfile(t *testing.T) {
	get, post := subtitlePlaybackInfoSteps(t)

	wrongFixture := get
	wrongFixture.Path = "/Items/7e897749137a8161f7a084cfad246bd3/PlaybackInfo"
	if _, _, err := requireSubtitlePlaybackInfoSteps([]Step{wrongFixture, post}); err == nil {
		t.Fatal("subtitle playback contract accepted a non-subtitle fixture")
	}

	wrongProfile := post
	wrongProfile.BodyFile = "../internal/playback/testdata/DeviceProfile-Chrome.json"
	if _, _, err := requireSubtitlePlaybackInfoSteps([]Step{get, wrongProfile}); err == nil {
		t.Fatal("subtitle playback contract accepted a DeviceProfile without the direct external SRT fixture")
	}
}

func subtitlePlaybackInfoSteps(t *testing.T) (Step, Step) {
	t.Helper()
	scenarios, err := loadScenarios(filepath.Join("..", "..", "corpora", "playbackinfo.yaml"))
	if err != nil {
		t.Fatalf("load committed playbackinfo corpus: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("committed playbackinfo corpus scenarios = %d, want one", len(scenarios))
	}
	get, post, err := requireSubtitlePlaybackInfoSteps(scenarios[0].Steps)
	if err != nil {
		t.Fatal(err)
	}
	return get, post
}

func requireSubtitlePlaybackInfoSteps(steps []Step) (Step, Step, error) {
	var get, post Step
	getFound, postFound := false, false
	for _, step := range steps {
		switch step.Name {
		case "Subtitle Sample GET PlaybackInfo":
			get, getFound = step, true
		case "Subtitle Sample POST DeviceProfile DirectMedia PlaybackInfo":
			post, postFound = step, true
		}
	}
	if !getFound || !postFound {
		return Step{}, Step{}, errors.New("playbackinfo subtitle GET and DeviceProfile POST steps are missing")
	}

	path := "/Items/" + subtitleSamplePlaybackInfoID + "/PlaybackInfo"
	if get.Method != http.MethodGet || get.Path != path || get.OperationID != "GetPlaybackInfo" {
		return Step{}, Step{}, errors.New("subtitle GET does not target the stock Subtitle Sample PlaybackInfo operation")
	}
	if post.Method != http.MethodPost || post.Path != path || post.OperationID != "GetPostedPlaybackInfo" {
		return Step{}, Step{}, errors.New("subtitle DeviceProfile POST does not target the stock Subtitle Sample PlaybackInfo operation")
	}
	for _, step := range []Step{get, post} {
		if step.AuthRole != "admin" || !reflect.DeepEqual(step.Headers, map[string]string{"Accept": "application/json"}) {
			return Step{}, Step{}, errors.New("subtitle PlaybackInfo steps must retain the authenticated JSON request contract")
		}
		if len(step.Normalize) != 0 || !reflect.DeepEqual(step.Normalizers, subtitlePlaybackInfoNormalizers) {
			return Step{}, Step{}, errors.New("subtitle PlaybackInfo steps must use only the declared ID, session, source, and transport normalizers")
		}
	}
	if post.BodyFile != "../internal/playback/testdata/DeviceProfile-DirectMedia.json" || post.BodyFileField != "DeviceProfile" || !post.bodyFileLoaded {
		return Step{}, Step{}, errors.New("subtitle DeviceProfile POST must use the pinned DirectMedia fixture")
	}
	body, ok := post.Body.(map[string]any)
	if !ok || len(body) != 1 {
		return Step{}, Step{}, errors.New("subtitle DeviceProfile POST body must contain only DeviceProfile")
	}
	profile, ok := body["DeviceProfile"].(map[string]any)
	if !ok || profile["Name"] != "Jellyfin Media Player" {
		return Step{}, Step{}, errors.New("subtitle DeviceProfile POST did not load the DirectMedia profile")
	}
	return get, post, nil
}
