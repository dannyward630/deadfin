// SPDX-License-Identifier: GPL-2.0-only

package playstate_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

// A report is ordered when its authenticated live-session transition succeeds,
// not when it later reaches persistence. An older report that was delayed
// before its durable mutation must not overwrite a newer accepted report's
// resume position or remembered stream selections.
func TestServiceProgressCommitsDurableStateInAcceptedSessionOrder(t *testing.T) {
	fixture := newDurableCommitFixture(t)
	older := progressReport(fixture.item.ID, ticks(120), 1, 2)
	newer := progressReport(fixture.item.ID, ticks(240), 3, 4)
	olderDone := make(chan error, 1)

	go func() {
		olderDone <- fixture.service.Progress(t.Context(), fixture.actor, older)
	}()
	<-fixture.items.firstMutationStarted
	assertLiveProgress(t, fixture.sessions, fixture.actor, ticks(120), 1, 2)

	if err := fixture.service.Progress(t.Context(), fixture.actor, newer); err != nil {
		t.Fatalf("commit newer progress report: %v", err)
	}
	assertLiveProgress(t, fixture.sessions, fixture.actor, ticks(240), 3, 4)

	fixture.releaseFirstMutation()
	if err := <-olderDone; err != nil {
		t.Fatalf("finish older progress report: %v", err)
	}

	got := fixture.items.userData(fixture.actor.UserID)
	if got.PositionTicks != ticks(240) || got.AudioStreamIndex == nil || *got.AudioStreamIndex != 3 || got.SubtitleStreamIndex == nil || *got.SubtitleStreamIndex != 4 {
		t.Fatalf("durable state after older report released = %#v; want newer accepted report position %d, audio 3, subtitle 4", got, ticks(240))
	}
}

// Reauthentication invalidates the old target at commit time as well as at the
// start of a report. A request that was already past its live transition when
// the identity was replaced must not mutate the former user's durable data.
func TestServiceProgressDoesNotCommitAfterTargetIsReauthenticated(t *testing.T) {
	fixture := newDurableCommitFixture(t)
	before := fixture.items.userData(fixture.actor.UserID)
	report := progressReport(fixture.item.ID, ticks(120), 1, 2)
	reportDone := make(chan error, 1)

	go func() {
		reportDone <- fixture.service.Progress(t.Context(), fixture.actor, report)
	}()
	<-fixture.items.firstMutationStarted
	assertLiveProgress(t, fixture.sessions, fixture.actor, ticks(120), 1, 2)

	replacementUser := dto.UUID{0x92}
	replacement, err := fixture.sessions.LogActivity(t.Context(), session.Activity{
		Client: fixture.actor.Client, Version: "2.0.0", DeviceID: fixture.actor.DeviceID, DeviceName: "Reauthenticated device", UserID: replacementUser,
	})
	if err != nil {
		t.Fatalf("reauthenticate same client/device: %v", err)
	}
	if replacement.Target == fixture.actor.Target || replacement.UserID != replacementUser {
		t.Fatalf("replacement session = %#v; want new target and user %s", replacement, replacementUser)
	}

	fixture.releaseFirstMutation()
	if err := <-reportDone; err != nil {
		t.Fatalf("finish displaced progress report: %v", err)
	}

	if got := fixture.items.userData(fixture.actor.UserID); !reflect.DeepEqual(got, before) {
		t.Fatalf("report displaced before durable commit mutated old user data = %#v; want %#v", got, before)
	}
}

// A commit lease must prevent a later report from replacing the live session
// after A has acquired its commit-time validation and before A's durable write.
// The fake pauses in that protected interval, after both A's mutation callback
// and lease acquisition have completed.
func TestServiceStartCommitsOlderReportBeforeLaterReportCanReplaceIt(t *testing.T) {
	fixture := newDurableCommitFixtureAfterCommitGuardAcquired(t)
	before := fixture.items.userData(fixture.actor.UserID)
	olderDone := make(chan error, 1)

	go func() {
		olderDone <- fixture.service.Start(t.Context(), fixture.actor, startReport(fixture.item.ID))
	}()
	<-fixture.items.firstMutationCommitGuardAcquired

	fixture.clock.Advance(time.Second)
	newerDone := make(chan error, 1)
	go func() {
		newerDone <- fixture.service.Start(t.Context(), fixture.actor, startReport(fixture.item.ID))
	}()
	assertReportIsBlocked(t, newerDone, "newer start report replaced an older report while its commit lease was held")

	fixture.releaseFirstDurableCommit()
	if err := <-olderDone; err != nil {
		t.Fatalf("finish older start report: %v", err)
	}
	if err := <-newerDone; err != nil {
		t.Fatalf("finish newer start report: %v", err)
	}

	got := fixture.items.userData(fixture.actor.UserID)
	if got.PlayCount != before.PlayCount+2 || got.LastPlayed == nil || !got.LastPlayed.Equal(fixture.clock.Now()) {
		t.Errorf("durable state after ordered reports = %#v; want both reports committed with the newer report's timestamp", got)
	}
	change := playstate.UserDataChanged{UserID: fixture.actor.UserID, ItemID: fixture.item.ID}
	assertUserDataChanged(t, fixture.subscription.Events(), []playstate.UserDataChanged{change, change})
}

// Reauthentication is another session transition and likewise must wait for
// A's already-acquired commit lease. Once A writes and publishes, the target
// replacement may complete without invalidating that completed commit.
func TestServiceStartCommitsReportBeforeTargetCanBeReauthenticated(t *testing.T) {
	fixture := newDurableCommitFixtureAfterCommitGuardAcquired(t)
	before := fixture.items.userData(fixture.actor.UserID)
	reportDone := make(chan error, 1)

	go func() {
		reportDone <- fixture.service.Start(t.Context(), fixture.actor, startReport(fixture.item.ID))
	}()
	<-fixture.items.firstMutationCommitGuardAcquired

	type activityResult struct {
		snapshot session.Snapshot
		err      error
	}
	replacementUser := dto.UUID{0x92}
	reauthenticationDone := make(chan activityResult, 1)
	go func() {
		snapshot, err := fixture.sessions.LogActivity(t.Context(), session.Activity{
			Client: fixture.actor.Client, Version: "2.0.0", DeviceID: fixture.actor.DeviceID, DeviceName: "Reauthenticated device", UserID: replacementUser,
		})
		reauthenticationDone <- activityResult{snapshot: snapshot, err: err}
	}()
	for range 1_024 {
		select {
		case result := <-reauthenticationDone:
			t.Fatalf("reauthenticated target while older report held its commit lease: snapshot=%#v, err=%v", result.snapshot, result.err)
		default:
			runtime.Gosched()
		}
	}

	fixture.releaseFirstDurableCommit()
	if err := <-reportDone; err != nil {
		t.Fatalf("finish older start report: %v", err)
	}
	replacement := <-reauthenticationDone
	if replacement.err != nil {
		t.Fatalf("reauthenticate same client/device: %v", replacement.err)
	}
	if replacement.snapshot.Target == fixture.actor.Target || replacement.snapshot.UserID != replacementUser {
		t.Fatalf("replacement session = %#v; want new target and user %s", replacement.snapshot, replacementUser)
	}

	got := fixture.items.userData(fixture.actor.UserID)
	if got.PlayCount != before.PlayCount+1 || got.LastPlayed == nil {
		t.Errorf("report did not commit before target replacement = %#v; want one recorded play", got)
	}
	assertUserDataChanged(t, fixture.subscription.Events(), []playstate.UserDataChanged{{UserID: fixture.actor.UserID, ItemID: fixture.item.ID}})
}

// A session with multiple users must release each completed user's lease
// before entering the next durable mutation. Otherwise a later report cannot
// advance the session while A is waiting to commit the second user.
func TestServiceStartDoesNotHoldFirstUserCommitLeaseWhileMutatingSecondUser(t *testing.T) {
	fixture := newDurableCommitFixtureWithSecondUserBlockedBeforeGuard(t)
	waitContext, cancelWait := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelWait()

	older := startDurableReportWorker(t.Context(), "older", func(ctx context.Context) error {
		return fixture.service.Start(ctx, fixture.actor, startReport(fixture.item.ID))
	})
	var newer *durableReportWorker
	defer func() {
		workers := []*durableReportWorker{older, newer}
		if !durableReportWorkersOutstanding(workers...) {
			return
		}
		fixture.releaseFirstMutation()
		fixture.releaseFirstDurableCommit()
		fixture.releaseMutationBeforeGuard()
		joinContext, cancelJoin := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelJoin()
		for _, err := range joinDurableReportWorkers(joinContext, workers...) {
			t.Errorf("report worker cleanup: %v", err)
		}
	}()

	fixture.releaseFirstMutation()
	<-fixture.items.mutationBeforeGuardStarted

	newer = startDurableReportWorker(t.Context(), "newer", func(ctx context.Context) error {
		return fixture.service.Start(ctx, fixture.actor, startReport(fixture.item.ID))
	})
	// Mutation 0 is the older primary user. Mutation 1 is still blocked.
	// Mutations 2 and 3 prove the newer report committed both users first.
	for _, want := range []int{0, 2, 3} {
		got, err := receiveDurableMutation(waitContext, fixture.items.durableMutationCompleted, want, newer)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			if newer.completed {
				t.Fatalf("durable mutation completion order = %d, want %d; newer report result error=%v", got, want, newer.result)
			}
			t.Fatalf("durable mutation completion order = %d, want %d", got, want)
		}
	}
	newerResult, err := newer.receive(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if newerResult != nil {
		t.Fatalf("commit newer multi-user start report: %v", newerResult)
	}

	fixture.releaseMutationBeforeGuard()
	olderResult, err := older.receive(waitContext)
	if err != nil {
		t.Fatal(err)
	}
	if olderResult != nil {
		t.Fatalf("finish older multi-user start report: %v", olderResult)
	}

	primary := fixture.items.userData(fixture.actor.UserID)
	if primary.PlayCount != 2 {
		t.Errorf("primary durable state after interleaved reports = %#v; want two committed starts", primary)
	}
	additional := fixture.items.userData(fixture.additionalUserID)
	if additional.PlayCount != 1 {
		t.Errorf("additional durable state after interleaved reports = %#v; want only the newer report", additional)
	}
	primaryChange := playstate.UserDataChanged{UserID: fixture.actor.UserID, ItemID: fixture.item.ID}
	additionalChange := playstate.UserDataChanged{UserID: fixture.additionalUserID, ItemID: fixture.item.ID}
	assertUserDataChanged(t, fixture.subscription.Events(), []playstate.UserDataChanged{primaryChange, primaryChange, additionalChange})
}

func TestReceiveDurableMutationReportsEarlyWorkerResult(t *testing.T) {
	earlyError := errors.New("guard returned early")
	for _, test := range []struct {
		name   string
		result error
		want   string
	}{
		{
			name: "nil result",
			want: "newer report returned before durable mutation 2: result error=<nil>",
		},
		{
			name:   "error result",
			result: earlyError,
			want:   "newer report returned before durable mutation 2: result error=guard returned early",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			done := make(chan error, 1)
			done <- test.result
			newer := &durableReportWorker{name: "newer", done: done, started: true}

			_, err := receiveDurableMutation(t.Context(), make(chan int), 2, newer)
			if err == nil {
				t.Fatal("early newer report result was not reported")
			}
			if err.Error() != test.want {
				t.Errorf("early newer report diagnostic = %q, want %q", err, test.want)
			}
			if !newer.completed || !errors.Is(newer.result, test.result) {
				t.Errorf("newer worker after early result = %#v, want completed with %v", newer, test.result)
			}
		})
	}
}

func TestJoinDurableReportWorkersWaitsForEveryStartedWorker(t *testing.T) {
	release := make(chan struct{})
	worker := func(name string) *durableReportWorker {
		return startDurableReportWorker(t.Context(), name, func(ctx context.Context) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	older := worker("older")
	newer := worker("newer")
	close(release)

	joinContext, cancelJoin := context.WithTimeout(t.Context(), time.Second)
	defer cancelJoin()
	if failures := joinDurableReportWorkers(joinContext, older, newer); len(failures) != 0 {
		t.Fatalf("join durable report workers: %v", failures)
	}
	if !older.completed || !newer.completed {
		t.Errorf("joined workers completed = older %t, newer %t; want both true", older.completed, newer.completed)
	}
}

type durableCommitFixture struct {
	items              *blockingItemStore
	sessions           *session.Manager
	service            playstate.Application
	clock              *clock.Fake
	subscription       *events.Subscription[playstate.UserDataChanged]
	item               item.Item
	actor              playstate.Actor
	additionalUserID   dto.UUID
	release            func()
	releaseCommit      func()
	releaseBeforeGuard func()
}

func newDurableCommitFixture(t *testing.T) durableCommitFixture {
	t.Helper()
	return newDurableCommitFixtureWithCommitBarrier(t, false, nil)
}

func newDurableCommitFixtureAfterCommitGuardAcquired(t *testing.T) durableCommitFixture {
	t.Helper()
	return newDurableCommitFixtureWithCommitBarrier(t, true, nil)
}

func newDurableCommitFixtureWithSecondUserBlockedBeforeGuard(t *testing.T) durableCommitFixture {
	t.Helper()
	additionalUserID := dto.UUID{0x93}
	fixture := newDurableCommitFixtureWithCommitBarrier(t, false, []dto.UUID{additionalUserID})
	fixture.items.blockMutationBeforeCommitGuard(1)
	fixture.additionalUserID = additionalUserID
	return fixture
}

func newDurableCommitFixtureWithCommitBarrier(t *testing.T, blockFirstCommitAfterGuard bool, additionalUserIDs []dto.UUID) durableCommitFixture {
	t.Helper()

	fakeClock := clock.NewFake(time.Date(2026, time.July, 27, 18, 0, 0, 0, time.UTC))
	itemID := dto.UUID{0x91}
	runtime := ticks(1_000)
	persistedItem := item.Item{ID: itemID, Kind: item.KindMovie, RunTimeTicks: &runtime}
	userID := dto.UUID{0x90}
	items := newBlockingItemStore(persistedItem, store.UserData{UserID: userID, ItemID: itemID, CustomKey: "concurrent"}, blockFirstCommitAfterGuard)
	sessions := session.New(fakeClock)
	actor := playstate.Actor{UserID: userID, Client: "Concurrent Client", DeviceID: "concurrent-device"}
	authenticated, err := sessions.LogActivity(t.Context(), session.Activity{
		Client: actor.Client, Version: "1.0.0", DeviceID: actor.DeviceID, DeviceName: "Concurrent device", UserID: actor.UserID, AdditionalUserIDs: additionalUserIDs,
	})
	if err != nil {
		t.Fatalf("authenticate fixture session: %v", err)
	}
	actor.Target = authenticated.Target
	changes := events.NewBus[playstate.UserDataChanged](fakeClock)
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 4})
	t.Cleanup(func() {
		if err := changes.Close(context.Background()); err != nil {
			t.Errorf("close user-data event bus: %v", err)
		}
	})
	configuration := durableCommitConfiguration{system: config.ServerConfiguration{
		MinResumePct: 0, MaxResumePct: 100, MinResumeDurationSeconds: 0,
	}}
	users := durableCommitUsers{byID: map[dto.UUID]store.User{
		userID: {ID: userID, Configuration: []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true}`)},
		{0x92}: {ID: dto.UUID{0x92}, Configuration: []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true}`)},
		{0x93}: {ID: dto.UUID{0x93}, Configuration: []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true}`)},
	}}
	service := playstate.NewService(fakeClock, configuration, items, users, sessions, changes)

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(items.releaseFirstMutation) })
	}
	var releaseCommitOnce sync.Once
	releaseCommit := func() {
		releaseCommitOnce.Do(func() { close(items.releaseFirstDurableCommit) })
	}
	var releaseBeforeGuardOnce sync.Once
	releaseBeforeGuard := func() {
		releaseBeforeGuardOnce.Do(func() { close(items.releaseMutationBeforeGuard) })
	}
	t.Cleanup(release)
	t.Cleanup(releaseCommit)
	t.Cleanup(releaseBeforeGuard)
	return durableCommitFixture{items: items, sessions: sessions, service: service, clock: fakeClock, subscription: subscription, item: persistedItem, actor: actor, release: release, releaseCommit: releaseCommit, releaseBeforeGuard: releaseBeforeGuard}
}

func (fixture durableCommitFixture) releaseFirstMutation() {
	fixture.release()
}

func (fixture durableCommitFixture) releaseFirstDurableCommit() {
	fixture.releaseCommit()
}

func (fixture durableCommitFixture) releaseMutationBeforeGuard() {
	fixture.releaseBeforeGuard()
}

type durableCommitConfiguration struct{ system config.ServerConfiguration }

func (configuration durableCommitConfiguration) System() config.ServerConfiguration {
	return configuration.system
}

type durableCommitUsers struct{ byID map[dto.UUID]store.User }

func (users durableCommitUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	user, found := users.byID[id]
	if !found {
		return store.User{}, errors.New("durable commit test user not found")
	}
	return user, nil
}

type blockingItemStore struct {
	mu sync.Mutex

	item item.Item
	data map[dto.UUID]store.UserData

	blockFirstCommitAfterGuard       bool
	blockMutationBeforeGuardOrder    int
	mutationCount                    int
	firstMutationStarted             chan struct{}
	releaseFirstMutation             chan struct{}
	firstMutationCommitGuardAcquired chan struct{}
	releaseFirstDurableCommit        chan struct{}
	mutationBeforeGuardStarted       chan struct{}
	releaseMutationBeforeGuard       chan struct{}
	durableMutationCompleted         chan int
}

func newBlockingItemStore(value item.Item, initial store.UserData, blockFirstCommitAfterGuard bool) *blockingItemStore {
	return &blockingItemStore{
		item:                             value,
		data:                             map[dto.UUID]store.UserData{initial.UserID: initial},
		blockFirstCommitAfterGuard:       blockFirstCommitAfterGuard,
		blockMutationBeforeGuardOrder:    -1,
		firstMutationStarted:             make(chan struct{}),
		releaseFirstMutation:             make(chan struct{}),
		firstMutationCommitGuardAcquired: make(chan struct{}),
		releaseFirstDurableCommit:        make(chan struct{}),
		mutationBeforeGuardStarted:       make(chan struct{}),
		releaseMutationBeforeGuard:       make(chan struct{}),
		durableMutationCompleted:         make(chan int, 8),
	}
}

func (items *blockingItemStore) blockMutationBeforeCommitGuard(order int) {
	items.mu.Lock()
	items.blockMutationBeforeGuardOrder = order
	items.mu.Unlock()
}

func (items *blockingItemStore) Get(_ context.Context, id dto.UUID) (item.Item, error) {
	if id != items.item.ID {
		return item.Item{}, errors.New("durable commit test item not found")
	}
	return items.item, nil
}

func (*blockingItemStore) MediaSourceRecords(context.Context, dto.UUID) ([]playback.MediaSourceRecord, error) {
	return nil, nil
}

func (items *blockingItemStore) MutateUserDataForItem(ctx context.Context, userID, itemID dto.UUID, mutate func(*store.UserData) error) (store.UserData, error) {
	data, _, err := items.mutate(ctx, userID, itemID, func(data *store.UserData) (bool, error) {
		return false, mutate(data)
	}, func() bool { return true })
	return data, err
}

func (items *blockingItemStore) MutateUserDataForItemAndVersions(ctx context.Context, userID, itemID dto.UUID, _ []dto.UUID, mutate func(*store.UserData) (bool, error)) (store.UserData, error) {
	data, _, err := items.mutate(ctx, userID, itemID, mutate, func() bool { return true })
	return data, err
}

func (items *blockingItemStore) MutateUserDataForItemGuarded(ctx context.Context, userID, itemID dto.UUID, mutate func(*store.UserData) error, guard func() bool) (store.UserData, bool, error) {
	return items.mutate(ctx, userID, itemID, func(data *store.UserData) (bool, error) {
		return false, mutate(data)
	}, guard)
}

func (items *blockingItemStore) MutateUserDataForItemAndVersionsGuarded(ctx context.Context, userID, itemID dto.UUID, _ []dto.UUID, mutate func(*store.UserData) (bool, error), guard func() bool) (store.UserData, bool, error) {
	return items.mutate(ctx, userID, itemID, mutate, guard)
}

func (items *blockingItemStore) mutate(_ context.Context, userID, itemID dto.UUID, mutate func(*store.UserData) (bool, error), guard func() bool) (store.UserData, bool, error) {
	items.mu.Lock()
	order := items.mutationCount
	items.mutationCount++
	blockAfterGuard := order == 0 && items.blockFirstCommitAfterGuard
	blockBeforeGuard := order == items.blockMutationBeforeGuardOrder
	items.mu.Unlock()

	if order == 0 && !blockAfterGuard {
		close(items.firstMutationStarted)
		<-items.releaseFirstMutation
	}

	items.mu.Lock()
	data := items.data[userID]
	items.mu.Unlock()
	if data.UserID.IsNil() {
		data = store.UserData{UserID: userID, ItemID: itemID, CustomKey: "concurrent"}
	}
	if _, err := mutate(&data); err != nil {
		return store.UserData{}, false, err
	}
	if blockBeforeGuard {
		close(items.mutationBeforeGuardStarted)
		<-items.releaseMutationBeforeGuard
	}

	if !guard() {
		return store.UserData{}, false, nil
	}
	if blockAfterGuard {
		close(items.firstMutationCommitGuardAcquired)
		<-items.releaseFirstDurableCommit
	}

	items.mu.Lock()
	items.data[userID] = data
	items.mu.Unlock()
	items.durableMutationCompleted <- order
	return data, true, nil
}

func (items *blockingItemStore) userData(userID dto.UUID) store.UserData {
	items.mu.Lock()
	defer items.mu.Unlock()
	return items.data[userID]
}

func progressReport(itemID dto.UUID, position int64, audio, subtitle int) playstate.Report {
	return playstate.Report{PlaybackState: session.PlaybackState{
		ItemID: itemID, PositionTicks: &position, AudioStreamIndex: &audio, SubtitleStreamIndex: &subtitle,
	}}
}

func startReport(itemID dto.UUID) playstate.Report {
	return playstate.Report{PlaybackState: session.PlaybackState{ItemID: itemID}}
}

func assertReportIsBlocked(t *testing.T, done <-chan error, message string) {
	t.Helper()
	for range 1_024 {
		select {
		case err := <-done:
			t.Fatalf("%s: %v", message, err)
		default:
			runtime.Gosched()
		}
	}
}

type durableReportWorker struct {
	name      string
	done      <-chan error
	started   bool
	completed bool
	result    error
}

func startDurableReportWorker(ctx context.Context, name string, run func(context.Context) error) *durableReportWorker {
	done := make(chan error, 1)
	worker := &durableReportWorker{name: name, done: done, started: true}
	go func() {
		done <- run(ctx)
	}()
	return worker
}

func (worker *durableReportWorker) receive(ctx context.Context) (error, error) {
	if worker.completed {
		return worker.result, nil
	}
	select {
	case result := <-worker.done:
		worker.completed = true
		worker.result = result
		return result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for %s report result: %w", worker.name, ctx.Err())
	}
}

func receiveDurableMutation(ctx context.Context, completed <-chan int, want int, newer *durableReportWorker) (int, error) {
	for {
		select {
		case got := <-completed:
			return got, nil
		default:
		}
		if newer.completed {
			return 0, fmt.Errorf("%s report returned before durable mutation %d: result error=%v", newer.name, want, newer.result)
		}
		select {
		case got := <-completed:
			return got, nil
		case result := <-newer.done:
			newer.completed = true
			newer.result = result
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for durable mutation %d: %w", want, ctx.Err())
		}
	}
}

func joinDurableReportWorkers(ctx context.Context, workers ...*durableReportWorker) []error {
	var failures []error
	for _, worker := range workers {
		if worker == nil || !worker.started || worker.completed {
			continue
		}
		result, err := worker.receive(ctx)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if result != nil {
			failures = append(failures, fmt.Errorf("%s report result: %w", worker.name, result))
		}
	}
	return failures
}

func durableReportWorkersOutstanding(workers ...*durableReportWorker) bool {
	for _, worker := range workers {
		if worker != nil && worker.started && !worker.completed {
			return true
		}
	}
	return false
}

func assertLiveProgress(t *testing.T, manager *session.Manager, actor playstate.Actor, position int64, audio, subtitle int) {
	t.Helper()
	snapshot, found := manager.Session(actor.Client, actor.DeviceID)
	if !found || snapshot.NowPlaying == nil || snapshot.NowPlaying.PositionTicks == nil || *snapshot.NowPlaying.PositionTicks != position || snapshot.NowPlaying.AudioStreamIndex == nil || *snapshot.NowPlaying.AudioStreamIndex != audio || snapshot.NowPlaying.SubtitleStreamIndex == nil || *snapshot.NowPlaying.SubtitleStreamIndex != subtitle {
		t.Fatalf("live session after accepted progress = %#v, found=%t; want position %d, audio %d, subtitle %d", snapshot, found, position, audio, subtitle)
	}
}
