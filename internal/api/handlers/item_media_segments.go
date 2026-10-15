// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// mediaSegmentType is the upstream MediaSegmentType enum. The query binder
// accepts the names case-insensitively and the defined numeric values, as
// ASP.NET enum model binding does.
type mediaSegmentType string

var mediaSegmentTypes = [...]mediaSegmentType{"Unknown", "Commercial", "Preview", "Recap", "Outro", "Intro"}

func (t *mediaSegmentType) UnmarshalText(text []byte) error {
	value := string(text)
	if n, err := strconv.Atoi(value); err == nil {
		if n < 0 || n >= len(mediaSegmentTypes) {
			return fmt.Errorf("invalid media segment type %q", value)
		}
		*t = mediaSegmentTypes[n]
		return nil
	}
	for _, name := range mediaSegmentTypes {
		if strings.EqualFold(value, string(name)) {
			*t = name
			return nil
		}
	}
	return fmt.Errorf("invalid media segment type %q", value)
}

type mediaSegmentsQuery struct {
	IncludeSegmentTypes []mediaSegmentType `query:"includeSegmentTypes"`
}

// mediaSegmentDto is the upstream MediaSegmentDto. No MediaSegmentProviders
// exist (D-60), so the list is always empty.
type mediaSegmentDto struct {
	ID         dto.UUID         `json:"Id"`
	ItemID     dto.UUID         `json:"ItemId"`
	Type       mediaSegmentType `json:"Type"`
	StartTicks int64            `json:"StartTicks"`
	EndTicks   int64            `json:"EndTicks"`
}

// mediaSegments serves GET /MediaSegments/{itemId}, the stock
// MediaSegmentsController.GetItemSegments. The item lookup applies the
// authenticated user's visibility; a missing or hidden item is 404. With no
// segment providers the response is an empty query result.
func (h itemExtrasHandler) mediaSegments(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		internalItemExtrasError(w)
		return
	}
	itemID, ok := itemExtrasItemID(w, r)
	if !ok {
		return
	}
	// LibraryManager.GetItemById throws ArgumentException for Guid.Empty.
	if itemID == dto.NilUUID {
		badItemExtrasRequest(w)
		return
	}
	query := mediaSegmentsQuery{}
	if err := bind.QueryRequest(r, &query); err != nil {
		badItemExtrasRequest(w)
		return
	}
	// The operation has no userId parameter; visibility follows the caller.
	caller := ""
	user, ok := h.itemExtrasUser(w, r, &caller)
	if !ok {
		return
	}
	if _, ok := h.source(w, r, itemID, user); !ok {
		return
	}
	_ = render.JSON(w, r, http.StatusOK, dto.NewQueryResult[mediaSegmentDto]())
}
