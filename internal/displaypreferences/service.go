// SPDX-License-Identifier: GPL-2.0-only

package displaypreferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Store persists the opaque display-preference document for one user, item,
// and client tuple.
type Store interface {
	GetDisplayPreference(context.Context, dto.UUID, dto.UUID, string) (store.DisplayPreference, error)
	PutDisplayPreference(context.Context, store.DisplayPreference) error
}

// Service resolves display-preference IDs and owns their normalized JSON
// document lifecycle.
type Service struct {
	store Store
}

// New creates a display-preference service backed by store.
func New(store Store) (*Service, error) {
	if isNilStore(store) {
		return nil, validation("store", "is required")
	}
	return &Service{store: store}, nil
}

func isNilStore(store Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// ValidationError reports an invalid display-preference request before it
// reaches persistence.
type ValidationError struct {
	Field  string
	Reason string
}

func (err *ValidationError) Error() string {
	return "displaypreferences: " + err.Field + " " + err.Reason
}

func validation(field, reason string) error {
	return &ValidationError{Field: field, Reason: reason}
}

// Get returns the effective preferences for one user, item, and client tuple.
// A missing stored document is represented by the source-compatible defaults.
func (service *Service) Get(ctx context.Context, userID dto.UUID, id, client string) (dto.DisplayPreferences, error) {
	itemID, err := resolveRequest(ctx, userID, id, client)
	if err != nil {
		return dto.DisplayPreferences{}, err
	}

	persisted, err := service.store.GetDisplayPreference(ctx, userID, itemID, client)
	if errors.Is(err, store.ErrNotFound) {
		return defaultPreferences(itemID, client), nil
	}
	if err != nil {
		return dto.DisplayPreferences{}, err
	}

	preferences, err := decodePersistedPreferences(persisted.Data)
	if err != nil {
		return dto.DisplayPreferences{}, err
	}
	return normalizePersisted(preferences, itemID, client), nil
}

// Put normalizes and atomically replaces one user, item, and client document.
func (service *Service) Put(ctx context.Context, userID dto.UUID, id, client string, preferences dto.DisplayPreferences) error {
	itemID, err := resolveRequest(ctx, userID, id, client)
	if err != nil {
		return err
	}

	normalized, err := normalizeForPut(preferences, itemID, client)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("displaypreferences: encode document: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return service.store.PutDisplayPreference(ctx, store.DisplayPreference{
		UserID: userID,
		ItemID: itemID,
		Client: client,
		Data:   encoded,
	})
}

func resolveRequest(ctx context.Context, userID dto.UUID, id, client string) (dto.UUID, error) {
	if ctx == nil {
		return dto.UUID{}, validation("context", "is required")
	}
	if err := ctx.Err(); err != nil {
		return dto.UUID{}, err
	}
	if userID.IsNil() {
		return dto.UUID{}, validation("user ID", "is required")
	}
	if id == "" {
		return dto.UUID{}, validation("item ID", "is required")
	}
	if client == "" {
		return dto.UUID{}, validation("client", "is required")
	}
	itemID, err := ResolveID(id)
	if err != nil {
		return dto.UUID{}, validation("item ID", "is invalid")
	}
	return itemID, nil
}

func defaultPreferences(id dto.UUID, client string) dto.DisplayPreferences {
	preferences := dto.DefaultDisplayPreferences()
	preferences.ID = stringPointer(id.Dashed())
	preferences.Client = stringPointer(client)
	preferences.SortBy = stringPointer("SortName")
	preferences.CustomPrefs = map[string]*string{
		"chromecastVersion":          stringPointer("stable"),
		"skipForwardLength":          stringPointer("30000"),
		"skipBackLength":             stringPointer("10000"),
		"enableNextVideoInfoOverlay": stringPointer("False"),
		"tvhome":                     nil,
		"dashboardTheme":             nil,
	}
	return preferences
}

// decodePersistedPreferences deliberately avoids DisplayPreferences.UnmarshalJSON.
// That method initializes request bodies with source defaults, but stored documents
// are opaque client state and must retain omitted fields as omitted.
func decodePersistedPreferences(data []byte) (dto.DisplayPreferences, error) {
	type storedDisplayPreferences dto.DisplayPreferences
	var stored storedDisplayPreferences
	if err := json.Unmarshal(data, &stored); err != nil {
		return dto.DisplayPreferences{}, fmt.Errorf("displaypreferences: decode stored document: %w", err)
	}
	return dto.DisplayPreferences(stored), nil
}

// normalizePersisted canonicalizes server-owned route identity without adding
// values to client-owned persisted state.
func normalizePersisted(preferences dto.DisplayPreferences, id dto.UUID, client string) dto.DisplayPreferences {
	preferences.ID = stringPointer(id.Dashed())
	preferences.Client = stringPointer(client)
	preferences.SortBy = cloneString(preferences.SortBy)
	preferences.ViewType = cloneString(preferences.ViewType)
	preferences.IndexBy = cloneString(preferences.IndexBy)
	preferences.CustomPrefs = clonePreferences(preferences.CustomPrefs)
	return preferences
}

func normalizeForPut(preferences dto.DisplayPreferences, id dto.UUID, client string) (dto.DisplayPreferences, error) {
	// The request binder initializes new request bodies with source defaults.
	// The service must otherwise retain the supplied document as opaque state.
	return normalizePersisted(preferences, id, client), nil
}

func clonePreferences(preferences map[string]*string) map[string]*string {
	if preferences == nil {
		return nil
	}
	cloned := make(map[string]*string, len(preferences))
	for key, value := range preferences {
		cloned[key] = cloneString(value)
	}
	return cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func stringPointer(value string) *string {
	return &value
}
