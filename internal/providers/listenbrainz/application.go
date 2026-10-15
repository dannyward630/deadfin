// SPDX-License-Identifier: GPL-2.0-only

package listenbrainz

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/playstate"
)

type application struct {
	inner     playstate.Application
	scrobbler *Scrobbler
}

// Wrap consumes the existing P5 playstate application events without changing
// their service or transport seams.
func Wrap(inner playstate.Application, scrobbler *Scrobbler) playstate.Application {
	if inner == nil || scrobbler == nil {
		return inner
	}
	return application{inner: inner, scrobbler: scrobbler}
}

func (wrapped application) Start(ctx context.Context, actor playstate.Actor, report playstate.Report) error {
	if err := wrapped.inner.Start(ctx, actor, report); err != nil {
		return err
	}
	wrapped.scrobbler.enqueue(ctx, playbackStarted, actor, report)
	return nil
}

func (wrapped application) Progress(ctx context.Context, actor playstate.Actor, report playstate.Report) error {
	if err := wrapped.inner.Progress(ctx, actor, report); err != nil {
		return err
	}
	wrapped.scrobbler.enqueue(ctx, playbackProgressed, actor, report)
	return nil
}

func (wrapped application) Stop(ctx context.Context, actor playstate.Actor, report playstate.Report) error {
	if err := wrapped.inner.Stop(ctx, actor, report); err != nil {
		return err
	}
	wrapped.scrobbler.enqueue(ctx, playbackStopped, actor, report)
	return nil
}
