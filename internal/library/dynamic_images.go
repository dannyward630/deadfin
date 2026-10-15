// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// DynamicImageInput is the frozen input of the dynamic image providers for
// one scanned video: its library options, the image refresh intent, and the
// probed streams and attachments of its media source.
type DynamicImageInput struct {
	Options          LibraryOptions
	Image            RefreshMode
	ReplaceAllImages bool
	Streams          []playback.MediaStream
	Attachments      []playback.MediaAttachment
}

// DynamicImageRefresher runs the IDynamicImageProvider pass of
// ItemImageProvider.RefreshImages (Screen Grabber, Embedded Image Extractor)
// and records saved images in target.ImageInfos. A provider failure is the
// refresher's to log; only a context error is returned.
type DynamicImageRefresher interface {
	RefreshDynamicImages(ctx context.Context, input DynamicImageInput, target *item.Item) error
}

// runsDynamicImages ports MetadataService.GetNonLocalImageProviders for
// providers without IHasItemChangeMonitor: they run only when the image
// refresh mode is FullRefresh or the item is on its first refresh (no
// DateLastRefreshed). The image stage runs only above ValidationOnly. An
// unchanged item on a plain rescan therefore does no extraction.
func runsDynamicImages(refresh RefreshOptions, stored *item.Item) bool {
	if refresh.Image <= RefreshModeValidationOnly {
		return false
	}
	return refresh.Image == RefreshModeFullRefresh || stored == nil || stored.DateLastRefreshed == nil
}

// refreshDynamicImages runs the dynamic image providers for the planned
// candidates after media probing, so the providers read the current streams
// and the carried stored images. A candidate whose media payload this scan
// reused reads its stored streams and attachments.
func (s *Scanner) refreshDynamicImages(ctx context.Context, desired []item.Item, planned map[int]RefreshOptions, media []store.ScannedChildMedia, options LibraryOptions) error {
	if s.options.DynamicImages == nil || len(planned) == 0 {
		return nil
	}
	payloads := make(map[string]store.ScannedChildMedia, len(media))
	for _, payload := range media {
		payloads[payload.ItemID.String()] = payload
	}
	for index := range desired {
		refresh, found := planned[index]
		if !found {
			continue
		}
		input := DynamicImageInput{Options: options, Image: refresh.Image, ReplaceAllImages: refresh.ReplaceAllImages}
		if payload, found := payloads[desired[index].ID.String()]; found {
			input.Streams, input.Attachments = payload.Streams, payload.Attachments
		} else {
			streams, err := s.items.Streams(ctx, desired[index].ID)
			if err != nil && !store.IsNotFound(err) {
				return err
			}
			attachments, err := s.items.Attachments(ctx, desired[index].ID)
			if err != nil && !store.IsNotFound(err) {
				return err
			}
			input.Streams, input.Attachments = streams, attachments
		}
		if err := s.options.DynamicImages.RefreshDynamicImages(ctx, input, &desired[index]); err != nil {
			return err
		}
	}
	return nil
}
