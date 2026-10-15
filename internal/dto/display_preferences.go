// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"encoding/json"
	"fmt"

	"github.com/deadfin-org/deadfin/internal/jsonnum"
)

// DisplayPreferences is the per-client display state for a viewable item.
type DisplayPreferences struct {
	ID                 *string                           `json:"Id"`
	ViewType           *string                           `json:"ViewType,omitempty"`
	SortBy             *string                           `json:"SortBy"`
	IndexBy            *string                           `json:"IndexBy,omitempty"`
	RememberIndexing   bool                              `json:"RememberIndexing"`
	PrimaryImageHeight int                               `json:"PrimaryImageHeight"`
	PrimaryImageWidth  int                               `json:"PrimaryImageWidth"`
	CustomPrefs        map[string]*string                `json:"CustomPrefs"`
	ScrollDirection    DisplayPreferencesScrollDirection `json:"ScrollDirection"`
	ShowBackdrop       bool                              `json:"ShowBackdrop"`
	RememberSorting    bool                              `json:"RememberSorting"`
	SortOrder          DisplayPreferencesSortOrder       `json:"SortOrder"`
	ShowSidebar        bool                              `json:"ShowSidebar"`
	Client             *string                           `json:"Client"`
}

// UnmarshalJSON starts each request from Jellyfin's default document so a
// partial preference update retains the same default-valued fields as stock.
// An explicitly supplied false or zero still overwrites that default.
func (preferences *DisplayPreferences) UnmarshalJSON(data []byte) error {
	type wire DisplayPreferences
	*preferences = DefaultDisplayPreferences()
	return jsonnum.Unmarshal(data, (*wire)(preferences))
}

// DisplayPreferencesScrollDirection mirrors the named JSON enum emitted by
// Jellyfin's DisplayPreferencesDto.
type DisplayPreferencesScrollDirection string

const (
	DisplayPreferencesScrollDirectionHorizontal DisplayPreferencesScrollDirection = "Horizontal"
	DisplayPreferencesScrollDirectionVertical   DisplayPreferencesScrollDirection = "Vertical"
)

func (direction *DisplayPreferencesScrollDirection) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed := DisplayPreferencesScrollDirection(value)
	if parsed != DisplayPreferencesScrollDirectionHorizontal && parsed != DisplayPreferencesScrollDirectionVertical {
		return fmt.Errorf("invalid ScrollDirection %q", value)
	}
	*direction = parsed
	return nil
}

// DisplayPreferencesSortOrder mirrors the named JSON enum emitted by
// Jellyfin's DisplayPreferencesDto.
type DisplayPreferencesSortOrder string

const (
	DisplayPreferencesSortOrderAscending  DisplayPreferencesSortOrder = "Ascending"
	DisplayPreferencesSortOrderDescending DisplayPreferencesSortOrder = "Descending"
)

func (order *DisplayPreferencesSortOrder) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed := DisplayPreferencesSortOrder(value)
	if parsed != DisplayPreferencesSortOrderAscending && parsed != DisplayPreferencesSortOrderDescending {
		return fmt.Errorf("invalid SortOrder %q", value)
	}
	*order = parsed
	return nil
}

// DefaultDisplayPreferences returns DisplayPreferences' source defaults.
func DefaultDisplayPreferences() DisplayPreferences {
	return DisplayPreferences{
		PrimaryImageHeight: 250,
		PrimaryImageWidth:  250,
		ScrollDirection:    DisplayPreferencesScrollDirectionHorizontal,
		ShowBackdrop:       true,
		SortOrder:          DisplayPreferencesSortOrderAscending,
		CustomPrefs:        map[string]*string{},
	}
}
