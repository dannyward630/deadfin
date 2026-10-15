// SPDX-License-Identifier: GPL-2.0-only

package backfill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// failingItemEnumerator is an ItemEnumerator whose BackfillEnumerateItems
// returns a fixed non-context error. The pass records the error and Run
// returns a non-nil error.
type failingItemEnumerator struct {
	err error
}

func (f failingItemEnumerator) BackfillEnumerateItems(_ context.Context, _ dto.UUID, _ int) ([]item.Item, error) {
	return nil, f.err
}
func (f failingItemEnumerator) Get(_ context.Context, _ dto.UUID) (item.Item, error) {
	return item.Item{}, errors.New("not implemented")
}
func (f failingItemEnumerator) DerivedSourceSnapshot(_ context.Context, _ dto.UUID) (store.DerivedSource, error) {
	return store.DerivedSource{}, errors.New("not implemented")
}

// TestBackfillEnumerationFailure verifies that Run returns a non-nil error
// and preserves the partial report when item enumeration fails with a
// non-context error. The CLI must exit non-zero in this case.
func TestBackfillEnumerationFailure(t *testing.T) {
	ctx := t.Context()
	deps := Deps{
		Items:  failingItemEnumerator{err: errors.New("database is closed")},
		Native: nil,
	}
	pass := New(deps, clock.NewFake(time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)))
	var out bytes.Buffer
	err := pass.Run(ctx, &out)
	if err == nil {
		t.Fatalf("Run returned nil error after enumeration failure, want non-nil")
	}
	var r Report
	if jErr := json.Unmarshal(out.Bytes(), &r); jErr != nil {
		t.Fatalf("decode report: %v\noutput: %s", jErr, out.String())
	}
	if r.EnumerationError == "" {
		t.Fatalf("report has no enumeration error, want the error text")
	}
}
