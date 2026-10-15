// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/probe"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

var applicationFFProbePath = "ffprobe"

func newApplicationMediaProbe(ctx context.Context) (library.MediaProbe, error) {
	prober, err := ffmpeg.NewProber(ctx, applicationFFProbePath)
	if err != nil {
		return nil, fmt.Errorf("open ffprobe: %w", err)
	}
	return applicationMediaProbe{prober: prober}, nil
}

type applicationMediaProbe struct{ prober ffmpeg.Prober }

func (p applicationMediaProbe) Probe(ctx context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	source, err := sourcefile.Open(input.Entry.Path, entryExpected(input.Entry))
	if err != nil {
		return library.MediaProbeResult{}, fmt.Errorf("pin scan source: %w", err)
	}
	defer func() { _ = source.Close() }()

	raw, err := p.prober.ProbeSourceAndValidate(ctx, source)
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	info, err := probe.Normalize(ctx, raw, probe.Options{
		IsAudio:   applicationProbeIsAudio(input.Item),
		Path:      input.Entry.Path,
		Protocol:  "File",
		VideoType: applicationProbeVideoType(input.Item),
	})
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	return applicationMediaProbeResult(info), nil
}

func applicationMediaProbeResult(info probe.MediaInfo) library.MediaProbeResult {
	result := library.MediaProbeResult{
		Container:         info.Container,
		Size:              info.Size,
		RunTimeTicks:      info.RunTimeTicks,
		TotalBitrate:      info.Bitrate,
		Artists:           info.Artists,
		AlbumArtists:      info.AlbumArtists,
		Genres:            info.Genres,
		Studios:           info.Studios,
		People:            applicationProbePeople(info.People),
		ProviderIDs:       applicationProbeProviderIDs(info.ProviderIDs),
		Overview:          info.Overview,
		OfficialRating:    info.OfficialRating,
		IndexNumber:       info.IndexNumber,
		ParentIndexNumber: info.ParentIndexNumber,
		ProductionYear:    info.ProductionYear,
		PremiereDate:      info.PremiereDate,
		Streams:           info.MediaStreams,
		Attachments:       info.MediaAttachments,
	}
	if info.Name != nil {
		result.Name = *info.Name
	}
	if info.Album != nil {
		result.Album = *info.Album
	}
	if info.VideoType != nil {
		value := item.VideoType(*info.VideoType)
		result.VideoType = &value
	}
	if info.IsoType != nil {
		value := item.IsoType(*info.IsoType)
		result.IsoType = &value
	}
	if info.Video3DFormat != nil {
		value := item.Video3DFormat(*info.Video3DFormat)
		result.Video3DFormat = &value
	}
	return result
}

func applicationProbePeople(values []probe.MediaPerson) []library.MediaProbePerson {
	result := make([]library.MediaProbePerson, 0, len(values))
	for _, value := range values {
		kind := applicationProbePersonKind(value.Type)
		if strings.TrimSpace(value.Name) == "" || kind == item.PersonKindUnknown {
			continue
		}
		result = append(result, library.MediaProbePerson{Name: value.Name, Role: copyApplicationProbeString(value.Role), Type: kind})
	}
	return result
}

func applicationProbePersonKind(value string) item.PersonKind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "actor":
		return item.PersonKindActor
	case "director":
		return item.PersonKindDirector
	case "composer":
		return item.PersonKindComposer
	case "writer":
		return item.PersonKindWriter
	case "producer":
		return item.PersonKindProducer
	case "conductor":
		return item.PersonKindConductor
	case "lyricist":
		return item.PersonKindLyricist
	case "arranger":
		return item.PersonKindArranger
	case "engineer":
		return item.PersonKindEngineer
	case "mixer":
		return item.PersonKindMixer
	case "remixer":
		return item.PersonKindRemixer
	default:
		return item.PersonKindUnknown
	}
}

func applicationProbeProviderIDs(values map[string]string) []library.MediaProbeProviderID {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]library.MediaProbeProviderID, 0, len(keys))
	for _, name := range keys {
		if value := strings.TrimSpace(values[name]); value != "" {
			result = append(result, library.MediaProbeProviderID{Name: name, Value: value})
		}
	}
	return result
}

func copyApplicationProbeString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func entryExpected(entry library.Entry) sourcefile.Expected {
	size := entry.Size
	expected := sourcefile.Expected{Size: &size}
	if !entry.Modified.IsZero() {
		modified := entry.Modified
		expected.ModifiedAt = &modified
	}
	if entry.Identity.Valid {
		inode := int64(entry.Identity.Inode)
		changed := entry.Identity.Changed
		expected.Inode, expected.ChangedAt = &inode, &changed
	}
	return expected
}

func applicationProbeIsAudio(value item.Item) bool {
	definition, found := item.Definition(value.Kind)
	return found && definition.DefaultMediaType == item.MediaTypeAudio
}

func applicationProbeVideoType(item.Item) string {
	return item.VideoTypeVideoFile.String()
}
