// SPDX-License-Identifier: GPL-2.0-only

package listenbrainz

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playstate"
)

const (
	defaultQueueSize   = 64
	fourMinutesInTicks = int64(4 * time.Minute / (100 * time.Nanosecond))
)

type ItemReader interface {
	Get(context.Context, dto.UUID) (item.Item, error)
}

type ErrorHandler func(error)

type ScrobblerOptions struct {
	Client           *Client
	Items            ItemReader
	Clock            clock.Clock
	SubmitPlayingNow bool
	QueueSize        int
	OnError          ErrorHandler
}

// Scrobbler owns a single bounded playback-event worker. Playback reports only
// enqueue after their durable P5 operation succeeds; external latency and
// failures never change the playstate response.
type Scrobbler struct {
	client           *Client
	items            ItemReader
	clock            clock.Clock
	submitPlayingNow bool
	onError          ErrorHandler

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan playbackEvent
	done   chan struct{}

	closeOnce sync.Once
}

type playbackEventKind uint8

const (
	playbackStarted playbackEventKind = iota
	playbackProgressed
	playbackStopped
	playbackBarrier
)

type playbackEvent struct {
	kind   playbackEventKind
	actor  playstate.Actor
	report playstate.Report
	at     time.Time
	done   chan struct{}
}

type playbackRecord struct {
	item         item.Item
	actor        playstate.Actor
	startedAt    time.Time
	position     int64
	submitted    bool
	scrobbleable bool
}

func NewScrobbler(options ScrobblerOptions) *Scrobbler {
	if options.Clock == nil {
		panic("listenbrainz: clock is required")
	}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	scrobbler := &Scrobbler{
		client:           options.Client,
		items:            options.Items,
		clock:            options.Clock,
		submitPlayingNow: options.SubmitPlayingNow,
		onError:          options.OnError,
	}
	if scrobbler.client == nil || !scrobbler.client.Enabled() || scrobbler.items == nil {
		return scrobbler
	}
	scrobbler.ctx, scrobbler.cancel = context.WithCancel(context.Background())
	scrobbler.queue = make(chan playbackEvent, queueSize)
	scrobbler.done = make(chan struct{})
	go scrobbler.run()
	return scrobbler
}

func (scrobbler *Scrobbler) enabled() bool {
	return scrobbler != nil && scrobbler.queue != nil
}

func (scrobbler *Scrobbler) enqueue(ctx context.Context, kind playbackEventKind, actor playstate.Actor, report playstate.Report) {
	if !scrobbler.enabled() {
		return
	}
	event := playbackEvent{kind: kind, actor: actor, report: report, at: scrobbler.clock.Now()}
	select {
	case scrobbler.queue <- event:
	case <-ctx.Done():
	case <-scrobbler.ctx.Done():
	default:
		scrobbler.reportError(errors.New("listenbrainz: playback event queue is full"))
	}
}

// Flush waits until all playback events enqueued before the call have been
// processed. It is a deterministic test/manual-tool seam, not a timer.
func (scrobbler *Scrobbler) Flush(ctx context.Context) error {
	if !scrobbler.enabled() {
		return nil
	}
	barrier := make(chan struct{})
	select {
	case scrobbler.queue <- playbackEvent{kind: playbackBarrier, done: barrier}:
	case <-ctx.Done():
		return ctx.Err()
	case <-scrobbler.ctx.Done():
		return errors.New("listenbrainz: scrobbler is closed")
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-scrobbler.ctx.Done():
		return errors.New("listenbrainz: scrobbler is closed")
	}
}

func (scrobbler *Scrobbler) Close(ctx context.Context) error {
	if !scrobbler.enabled() {
		return nil
	}
	var flushErr error
	scrobbler.closeOnce.Do(func() {
		flushErr = scrobbler.Flush(ctx)
		scrobbler.cancel()
	})
	select {
	case <-scrobbler.done:
		return flushErr
	case <-ctx.Done():
		return errors.Join(flushErr, ctx.Err())
	}
}

func (scrobbler *Scrobbler) run() {
	defer close(scrobbler.done)
	records := make(map[string]playbackRecord)
	for {
		select {
		case <-scrobbler.ctx.Done():
			return
		case event := <-scrobbler.queue:
			if event.kind == playbackBarrier {
				close(event.done)
				continue
			}
			scrobbler.process(records, event)
		}
	}
}

func (scrobbler *Scrobbler) process(records map[string]playbackRecord, event playbackEvent) {
	key := event.actor.Target.ID
	switch event.kind {
	case playbackStarted:
		value, err := scrobbler.items.Get(scrobbler.ctx, event.report.ItemID)
		if err != nil {
			scrobbler.reportError(err)
			return
		}
		record := playbackRecord{
			item:         value,
			actor:        event.actor,
			startedAt:    event.at,
			position:     reportPosition(event.report),
			scrobbleable: value.Kind == item.KindAudio && itemName(value) != "" && artistName(value) != "",
		}
		records[key] = record
		if record.scrobbleable && scrobbler.submitPlayingNow {
			if err := scrobbler.client.Submit(scrobbler.ctx, buildSubmission("playing_now", record, 0)); err != nil {
				scrobbler.reportError(err)
			}
		}
	case playbackProgressed:
		record, found := records[key]
		if !found {
			return
		}
		if !samePlayback(record, event) {
			delete(records, key)
			return
		}
		record.position = max(record.position, reportPosition(event.report))
		if record.scrobbleable && !record.submitted && reachedThreshold(record, event.at) {
			if err := scrobbler.client.Submit(scrobbler.ctx, buildSubmission("single", record, playedTicks(record, event.at))); err != nil {
				scrobbler.reportError(err)
			} else {
				record.submitted = true
			}
		}
		records[key] = record
	case playbackStopped:
		record, found := records[key]
		if !found {
			return
		}
		if !samePlayback(record, event) {
			delete(records, key)
			return
		}
		record.position = max(record.position, reportPosition(event.report))
		if record.scrobbleable && !record.submitted && !event.report.Failed && reachedThreshold(record, event.at) {
			if err := scrobbler.client.Submit(scrobbler.ctx, buildSubmission("single", record, playedTicks(record, event.at))); err != nil {
				scrobbler.reportError(err)
			}
		}
		delete(records, key)
	}
}

func samePlayback(record playbackRecord, event playbackEvent) bool {
	if record.actor.UserID != event.actor.UserID {
		return false
	}
	return event.report.ItemID.IsNil() || event.report.ItemID == record.item.ID
}

func (scrobbler *Scrobbler) reportError(err error) {
	if err != nil && scrobbler.onError != nil {
		scrobbler.onError(err)
	}
}

func reportPosition(report playstate.Report) int64 {
	if report.PositionTicks == nil || *report.PositionTicks < 0 {
		return 0
	}
	return *report.PositionTicks
}

func playedTicks(record playbackRecord, at time.Time) int64 {
	if record.position > 0 {
		return record.position
	}
	if elapsed := at.Sub(record.startedAt); elapsed > 0 {
		return int64(elapsed / (100 * time.Nanosecond))
	}
	return 0
}

func reachedThreshold(record playbackRecord, at time.Time) bool {
	threshold := fourMinutesInTicks
	if record.item.RunTimeTicks != nil && *record.item.RunTimeTicks > 0 {
		half := *record.item.RunTimeTicks / 2
		if half < threshold {
			threshold = half
		}
	}
	return playedTicks(record, at) >= threshold
}

func buildSubmission(listenType string, record playbackRecord, durationPlayedTicks int64) Submission {
	metadata := TrackMetadata{
		ArtistName:  artistName(record.item),
		TrackName:   itemName(record.item),
		ReleaseName: pointerString(record.item.Album),
		AdditionalInfo: AdditionalInfo{
			ArtistMBIDs:             artistMBIDs(record.item),
			ReleaseGroupMBID:        providerID(record.item, "MusicBrainzReleaseGroup"),
			ReleaseMBID:             providerID(record.item, "MusicBrainzAlbum"),
			RecordingMBID:           providerID(record.item, "MusicBrainzRecording"),
			TrackMBID:               providerID(record.item, "MusicBrainzTrack"),
			MediaPlayer:             record.actor.Client,
			SubmissionClient:        "Deadfin",
			SubmissionClientVersion: "dev",
		},
	}
	if record.item.IndexNumber != nil {
		metadata.AdditionalInfo.TrackNumber = strconv.Itoa(*record.item.IndexNumber)
	}
	if record.item.RunTimeTicks != nil && *record.item.RunTimeTicks > 0 {
		metadata.AdditionalInfo.DurationMS = *record.item.RunTimeTicks / 10_000
	}
	if durationPlayedTicks > 0 {
		metadata.AdditionalInfo.DurationPlayed = durationPlayedTicks / 10_000_000
	}
	listen := Listen{TrackMetadata: metadata}
	if listenType != "playing_now" {
		listenedAt := record.startedAt.UTC().Unix()
		listen.ListenedAt = &listenedAt
	}
	return Submission{ListenType: listenType, Payload: []Listen{listen}}
}

func artistName(value item.Item) string {
	for _, values := range [][]string{value.Values.Artists, value.Values.AlbumArtists} {
		for _, candidate := range values {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func itemName(value item.Item) string {
	return strings.TrimSpace(pointerString(value.Name))
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func providerID(value item.Item, name string) string {
	id, _ := value.ProviderIDs.Get(name)
	return strings.TrimSpace(id)
}

func artistMBIDs(value item.Item) []string {
	seen := make(map[string]struct{}, 2)
	result := make([]string, 0, 2)
	for _, name := range []string{"MusicBrainzArtist", "MusicBrainzAlbumArtist"} {
		id := providerID(value, name)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, id)
	}
	return result
}
