// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import "sync"

// PublicationCoordinator serializes a trickplay publication with readers that
// resolve its metadata and owned artifact. A caller supplies one shared instance
// to the generator and reader services; it contains no package-global state.
type PublicationCoordinator struct {
	mu sync.RWMutex
}

// NewPublicationCoordinator constructs the shared coordinator for one runtime.
func NewPublicationCoordinator() *PublicationCoordinator {
	return &PublicationCoordinator{}
}

// RLock acquires a read lease spanning metadata lookup and artifact open.
func (coordinator *PublicationCoordinator) RLock() {
	coordinator.mu.RLock()
}

// RUnlock releases a read lease acquired with RLock.
func (coordinator *PublicationCoordinator) RUnlock() {
	coordinator.mu.RUnlock()
}

// Lock acquires the exclusive publication lease spanning filesystem exchange
// and durable metadata commit or rollback.
func (coordinator *PublicationCoordinator) Lock() {
	coordinator.mu.Lock()
}

// Unlock releases an exclusive publication lease acquired with Lock.
func (coordinator *PublicationCoordinator) Unlock() {
	coordinator.mu.Unlock()
}
