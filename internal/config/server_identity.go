// SPDX-License-Identifier: GPL-2.0-only

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// ServerIdentity is the stable public identity advertised to Jellyfin clients.
// It is constructed at the composition root and injected into HTTP handlers.
type ServerIdentity struct {
	ServerName string
	ID         dto.UUID
	Version    string
}

// NewServerIdentity validates the fields clients require from /System/Info/Public.
func NewServerIdentity(serverName string, id dto.UUID, version string) (ServerIdentity, error) {
	identity := ServerIdentity{
		ServerName: serverName,
		ID:         id,
		Version:    version,
	}
	if err := identity.Validate(); err != nil {
		return ServerIdentity{}, err
	}
	return identity, nil
}

// Validate reports whether identity can be advertised to public Jellyfin clients.
func (identity ServerIdentity) Validate() error {
	if strings.TrimSpace(identity.ServerName) == "" {
		return errors.New("config: server name is required")
	}
	if identity.ID.IsNil() {
		return errors.New("config: server ID is required")
	}
	if strings.TrimSpace(identity.Version) == "" {
		return errors.New("config: server version is required")
	}
	return nil
}

// DefaultServerIdentity returns Deadfin's P0 process identity. It is used only
// by route-ledger composition that does not open a program-data root.
func DefaultServerIdentity() ServerIdentity {
	return ServerIdentity{
		ServerName: "Deadfin",
		ID: dto.UUID{
			0x6e, 0xc4, 0x5b, 0xa1, 0x41, 0x8d, 0x4f, 0x6b,
			0x8a, 0xe8, 0xe8, 0xc6, 0x4b, 0xc6, 0xb5, 0x98,
		},
		Version: "12.0.0",
	}
}

// deviceFile is the name of the persisted installation identifier, matching
// the pinned upstream mechanism at upstream/jellyfin/Emby.Server.Implementations/Devices/DeviceId.cs:38.
const deviceFile = "device.txt"

// LoadOrCreateServerIdentity loads the installation identity from the
// program-data root. On first start it generates a cryptographically random
// UUIDv4, persists it as data/device.txt, and returns a validated identity.
// A valid pre-existing device.txt is retained. A malformed or nil GUID is
// replaced with a valid persisted UUIDv4.
//
// The configured ServerName wins over the supplied hostname when it is
// non-empty after trimming. An empty configured name reports the hostname.
// The installation ID never changes after the first start.
func LoadOrCreateServerIdentity(paths Paths, configuredName, hostname string, entropy io.Reader) (ServerIdentity, error) {
	id, err := loadOrCreateDeviceID(paths.Data, entropy)
	if err != nil {
		return ServerIdentity{}, err
	}

	name := strings.TrimSpace(configuredName)
	if name == "" {
		name = hostname
	}

	return NewServerIdentity(name, id, "12.0.0")
}

// loadOrCreateDeviceID reads data/device.txt, validates the cached GUID, and
// generates plus persists a new UUIDv4 when the file is absent or invalid.
func loadOrCreateDeviceID(dataDir string, entropy io.Reader) (dto.UUID, error) {
	path := filepath.Join(dataDir, deviceFile)

	cached, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return dto.UUID{}, fmt.Errorf("config: read installation identity: %w", err)
	}
	if err == nil {
		if id, parseErr := dto.ParseUUID(string(cached)); parseErr == nil && !id.IsNil() {
			return id, nil
		}
		// Fall through to generate a new ID for a malformed or nil GUID.
	}

	id, err := newUUIDv4(entropy)
	if err != nil {
		return dto.UUID{}, fmt.Errorf("config: generate installation identity: %w", err)
	}

	contents := []byte(id.String())
	if _, err := writeAtomically(path, contents); err != nil {
		return dto.UUID{}, fmt.Errorf("config: persist installation identity: %w", err)
	}

	return id, nil
}

// newUUIDv4 fills 16 bytes from the supplied entropy source and sets the RFC
// 4122 version-4 and variant bits.
func newUUIDv4(entropy io.Reader) (dto.UUID, error) {
	var id dto.UUID
	if _, err := io.ReadFull(entropy, id[:]); err != nil {
		return dto.UUID{}, fmt.Errorf("read entropy: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40 // version 4
	id[8] = (id[8] & 0x3f) | 0x80 // variant RFC 4122
	return id, nil
}
