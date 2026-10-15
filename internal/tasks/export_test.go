// SPDX-License-Identifier: GPL-2.0-only

package tasks

// PendingTriggerForTest reports whether a coalesced run is pending for id and
// returns a copy of the retained pending trigger (nil for a manual coalesce).
// It reads the committed state under the scheduler's own locks and is the
// barrier tests use to prove startLocked committed a coalesce before they
// release the running execution.
func (s *Scheduler) PendingTriggerForTest(id string) (pending bool, trigger *TriggerInfo) {
	s.mu.Lock()
	t, ok := s.tasks[lowerID(id)]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pendingTrigger != nil {
		retained := *t.pendingTrigger
		trigger = &retained
	}
	return t.pending, trigger
}
