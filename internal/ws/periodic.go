// SPDX-License-Identifier: GPL-2.0-only

package ws

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

var errInvalidPeriodicInterval = errors.New("ws: periodic interval must be dueTimeMs,periodMs")

type periodicInterval struct {
	due    time.Duration
	period time.Duration
}

func parsePeriodicInterval(value string) (periodicInterval, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return periodicInterval{}, errInvalidPeriodicInterval
	}
	due, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || due < 0 {
		return periodicInterval{}, errInvalidPeriodicInterval
	}
	period, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || period <= 0 {
		return periodicInterval{}, errInvalidPeriodicInterval
	}
	return periodicInterval{due: time.Duration(due) * time.Millisecond, period: time.Duration(period) * time.Millisecond}, nil
}

type periodicControl struct {
	start *periodicInterval
	stop  bool
	ack   chan struct{}
}

type periodicFeed struct {
	clock  clock.Clock
	load   func(context.Context) (any, error)
	send   func(MessageType, any) bool
	kind   MessageType
	cancel context.CancelFunc

	control chan periodicControl
	notify  chan bool
	done    chan struct{}
}

func newPeriodicFeed(ctx context.Context, c clock.Clock, kind MessageType, load func(context.Context) (any, error), send func(MessageType, any) bool, cancel context.CancelFunc) *periodicFeed {
	feed := &periodicFeed{
		clock: c, kind: kind, load: load, send: send, cancel: cancel,
		control: make(chan periodicControl, 4),
		notify:  make(chan bool, 4),
		done:    make(chan struct{}),
	}
	go feed.run(ctx)
	return feed
}

func (feed *periodicFeed) start(interval periodicInterval) {
	ack := make(chan struct{})
	select {
	case feed.control <- periodicControl{start: &interval, ack: ack}:
		select {
		case <-ack:
		case <-feed.done:
		}
	case <-feed.done:
	}
}

func (feed *periodicFeed) stop() {
	ack := make(chan struct{})
	select {
	case feed.control <- periodicControl{stop: true, ack: ack}:
		select {
		case <-ack:
		case <-feed.done:
		}
	case <-feed.done:
	}
}

func (feed *periodicFeed) changed(force bool) {
	select {
	case feed.notify <- force:
	case <-feed.done:
	default:
		// Coalesce source changes. Preserve a force notification by replacing a
		// queued non-force marker where possible.
		if force {
			select {
			case <-feed.notify:
			default:
			}
			select {
			case feed.notify <- true:
			case <-feed.done:
			default:
			}
		}
	}
}

func (feed *periodicFeed) wait() {
	<-feed.done
}

func (feed *periodicFeed) run(ctx context.Context) {
	defer close(feed.done)
	var (
		active   bool
		interval periodicInterval
		lastSent time.Time
		timer    clock.Timer
		timerC   <-chan time.Time
	)
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
		}
		timerC = nil
	}
	resetTimer := func(delay time.Duration) {
		if timer == nil {
			timer = feed.clock.NewTimer(delay)
		} else {
			timer.Stop()
			timer.Reset(delay)
		}
		timerC = timer.C()
	}
	send := func() bool {
		data, err := feed.load(ctx)
		if err != nil {
			feed.cancel()
			return false
		}
		if !feed.send(feed.kind, data) {
			return false
		}
		lastSent = feed.clock.Now()
		return true
	}
	for {
		select {
		case <-ctx.Done():
			stopTimer()
			return
		case command := <-feed.control:
			switch {
			case command.start != nil:
				active = true
				interval = *command.start
				lastSent = time.Time{}
				resetTimer(interval.due)
			case command.stop:
				active = false
				stopTimer()
			}
			close(command.ack)
		case force := <-feed.notify:
			if !active {
				continue
			}
			now := feed.clock.Now()
			if force || lastSent.IsZero() || now.Sub(lastSent) >= interval.period {
				if !send() {
					return
				}
				resetTimer(interval.period)
			}
		case <-timerC:
			if !active {
				continue
			}
			if !send() {
				return
			}
			resetTimer(interval.period)
		}
	}
}
