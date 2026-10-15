// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// Library is the persisted registry record for one virtual media folder.
type Library struct {
	ID             dto.UUID
	Name           string
	Path           string
	CollectionType string
	Options        string
}

// LibraryStore persists the registry that connects portable library folders to
// their database identities. It serializes mutations because the inherited
// libraries table deliberately has no uniqueness index for name or path.
type LibraryStore struct {
	stores Stores
	mu     sync.Mutex
}

// NewLibraryStore constructs a typed library-registry adapter.
func NewLibraryStore(stores Stores) *LibraryStore { return &LibraryStore{stores: stores} }

const libraryMutationLock int64 = 0x4c696272617279

// Create adds a virtual-folder registry record. Name and path are each unique
// under the store's single-writer mutation contract.
func (s *LibraryStore) Create(ctx context.Context, library Library) (created Library, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.mutationTx(ctx)
	if err != nil {
		return Library{}, err
	}
	defer rollbackOnReturn(tx, &err)

	var found int
	if err := tx.QueryRowContext(ctx,
		"SELECT 1 FROM libraries WHERE name = "+s.p(1)+" OR path = "+s.p(2)+" LIMIT 1"+s.forUpdate(),
		library.Name, library.Path,
	).Scan(&found); err == nil {
		return Library{}, fmt.Errorf("create library: duplicate name or path")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Library{}, fmt.Errorf("check library uniqueness: %w", err)
	}

	query := "INSERT INTO libraries (id, name, path, collection_type, options) VALUES (" + s.placeholders(5) + ")"
	args := []any{s.uuidArg(library.ID), library.Name, library.Path, library.CollectionType, library.Options}
	if s.postgres() {
		query = "INSERT INTO libraries (id, name, path, collection_type, options) VALUES (" + s.p(1) + ", " + s.p(2) + ", " + s.p(3) + ", " + s.p(4) + ", to_jsonb(" + s.p(5) + "::text))"
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return Library{}, fmt.Errorf("insert library: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Library{}, err
	}
	return library, nil
}

// Update replaces a registry record after portable filesystem state changes.
func (s *LibraryStore) Update(ctx context.Context, library Library) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.mutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	var found int
	if err := tx.QueryRowContext(ctx,
		"SELECT 1 FROM libraries WHERE (name = "+s.p(1)+" OR path = "+s.p(2)+") AND id <> "+s.p(3)+" LIMIT 1"+s.forUpdate(),
		library.Name, library.Path, s.uuidArg(library.ID),
	).Scan(&found); err == nil {
		return fmt.Errorf("update library: duplicate name or path")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check library uniqueness: %w", err)
	}
	query := "UPDATE libraries SET name = " + s.p(1) + ", path = " + s.p(2) + ", collection_type = " + s.p(3) + ", options = " + s.p(4) + " WHERE id = " + s.p(5)
	args := []any{library.Name, library.Path, library.CollectionType, library.Options, s.uuidArg(library.ID)}
	if s.postgres() {
		query = "UPDATE libraries SET name = " + s.p(1) + ", path = " + s.p(2) + ", collection_type = " + s.p(3) + ", options = to_jsonb(" + s.p(4) + "::text) WHERE id = " + s.p(5)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update library: %w", err)
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns the registry record with id.
func (s *LibraryStore) Get(ctx context.Context, id dto.UUID) (Library, error) {
	library, err := s.scan(s.stores.Reader().QueryRowContext(ctx,
		"SELECT id, name, path, collection_type, "+s.optionsColumn()+" FROM libraries WHERE id = "+s.p(1),
		s.uuidArg(id),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Library{}, ErrNotFound
	}
	if err != nil {
		return Library{}, fmt.Errorf("get library: %w", err)
	}
	return library, nil
}

// List returns all registry records in deterministic name and ID order.
func (s *LibraryStore) List(ctx context.Context) (libraries []Library, err error) {
	rows, err := s.stores.Reader().QueryContext(ctx,
		"SELECT id, name, path, collection_type, "+s.optionsColumn()+" FROM libraries ORDER BY name, id")
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	libraries = make([]Library, 0)
	for rows.Next() {
		library, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan library: %w", err)
		}
		libraries = append(libraries, library)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate libraries: %w", err)
	}
	return libraries, nil
}

// Delete removes a registry record.
func (s *LibraryStore) Delete(ctx context.Context, id dto.UUID) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.mutationTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	result, err := tx.ExecContext(ctx, "DELETE FROM libraries WHERE id = "+s.p(1), s.uuidArg(id))
	if err != nil {
		return fmt.Errorf("delete library: %w", err)
	}
	if err := changed(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *LibraryStore) scan(row interface{ Scan(...any) error }) (Library, error) {
	var library Library
	var rawID any
	var collectionType sql.NullString
	if err := row.Scan(&rawID, &library.Name, &library.Path, &collectionType, &library.Options); err != nil {
		return Library{}, err
	}
	if err := scanIdentityUUID(&library.ID, rawID); err != nil {
		return Library{}, err
	}
	library.CollectionType = collectionType.String
	return library, nil
}

func (s *LibraryStore) mutationTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.stores.Writer().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	if s.postgres() {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock("+fmt.Sprintf("%d", libraryMutationLock)+")"); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
}

func (s *LibraryStore) p(index int) string { return s.stores.Dialect().Placeholder(index) }

func (s *LibraryStore) placeholders(count int) string {
	result := s.p(1)
	for index := 2; index <= count; index++ {
		result += ", " + s.p(index)
	}
	return result
}

func (s *LibraryStore) uuidArg(id dto.UUID) any { return uuidArgDialect(s.stores.Dialect(), id) }

func (s *LibraryStore) postgres() bool { return s.p(1) != "?" }

func (s *LibraryStore) forUpdate() string {
	if s.postgres() {
		return " FOR UPDATE"
	}
	return ""
}

func (s *LibraryStore) optionsColumn() string {
	if s.postgres() {
		return "options #>> '{}'"
	}
	return "options"
}
