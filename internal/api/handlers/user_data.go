// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/jsonnum"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/go-chi/chi/v5"
)

// UserDataMutator is the existing transactional item-store seam used by all
// user-data endpoints. Each callback changes only the endpoint-owned fields.
type UserDataMutator interface {
	MutateUserDataForItem(context.Context, dto.UUID, dto.UUID, func(*store.UserData) error) (store.UserData, error)
}

type userDataHandler struct {
	items   ItemReader
	mutator UserDataMutator
	users   ItemUserResolver
	clock   clock.Clock
}

type userDataQuery struct {
	UserID     *dto.UUID `query:"userId"`
	DatePlayed *dto.Time `query:"datePlayed"`
}

type optionalJSON[T any] struct {
	set   bool
	value *T
}

func (value *optionalJSON[T]) UnmarshalJSON(data []byte) error {
	value.set = true
	if string(data) == "null" {
		value.value = nil
		return nil
	}
	var decoded T
	if err := jsonnum.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.value = &decoded
	return nil
}

// apply mirrors the pinned upstream HasValue contract. A field that the
// request omitted or that it set to JSON null does not have a value, so the
// caller keeps the stored field. A field set to a non-null value applies that
// value, including the false and zero values.
func (value optionalJSON[T]) apply() (*T, bool) {
	if !value.set || value.value == nil {
		return nil, false
	}
	return value.value, true
}

type updateItemUserDataDTO struct {
	Rating                optionalJSON[float32]  `json:"Rating"`
	PlaybackPositionTicks optionalJSON[int64]    `json:"PlaybackPositionTicks"`
	PlayCount             optionalJSON[int]      `json:"PlayCount"`
	IsFavorite            optionalJSON[bool]     `json:"IsFavorite"`
	Likes                 optionalJSON[bool]     `json:"Likes"`
	LastPlayedDate        optionalJSON[dto.Time] `json:"LastPlayedDate"`
	Played                optionalJSON[bool]     `json:"Played"`
	present               bool
}

// UnmarshalJSON records that the request body was a present JSON object.
// json.Unmarshal calls this method with the raw token "null" for a JSON-null
// body, so a null body leaves present false. bind.JSONBody returns nil for an
// empty body before it unmarshals, so an absent body also leaves present
// false. A "{}" object calls this method with a non-null token, sets present
// true, and remains a valid no-op.
func (body *updateItemUserDataDTO) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	body.present = true
	type alias updateItemUserDataDTO
	return jsonnum.Unmarshal(data, (*alias)(body))
}

// RegisterUserData registers the modern user-data mutation families.
func RegisterUserData(rt *router.Router, mutator UserDataMutator, items ItemReader, users ItemUserResolver, wallClock clock.Clock) {
	h := userDataHandler{items: items, mutator: mutator, users: users, clock: wallClock}
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodPost, "/UserItems/{itemId}/UserData", router.Route{OperationID: "UpdateItemUserData", Policy: policy}, h.update)
	rt.Handle(http.MethodDelete, "/UserItems/{itemId}/UserData", router.Route{OperationID: "SHADOW:DeleteItemUserData", Policy: policy}, h.delete)
	rt.Handle(http.MethodPost, "/UserPlayedItems/{itemId}", router.Route{OperationID: "MarkPlayedItem", Policy: policy}, h.markPlayed)
	rt.Handle(http.MethodDelete, "/UserPlayedItems/{itemId}", router.Route{OperationID: "MarkUnplayedItem", Policy: policy}, h.markUnplayed)
	rt.Handle(http.MethodPost, "/UserFavoriteItems/{itemId}", router.Route{OperationID: "MarkFavoriteItem", Policy: policy}, h.markFavorite)
	rt.Handle(http.MethodDelete, "/UserFavoriteItems/{itemId}", router.Route{OperationID: "UnmarkFavoriteItem", Policy: policy}, h.unmarkFavorite)
}

func (h userDataHandler) update(w http.ResponseWriter, r *http.Request) {
	var body updateItemUserDataDTO
	if err := bind.JSONBody(r, &body); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, bind.ErrUnsupportedMediaType) {
			status = http.StatusUnsupportedMediaType
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	if !body.present {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	h.mutate(w, r, func(data *store.UserData, _ userDataQuery) {
		if value, ok := body.Rating.apply(); ok {
			data.Rating = value
		}
		if value, ok := body.PlaybackPositionTicks.apply(); ok {
			data.PositionTicks = *value
		}
		if value, ok := body.PlayCount.apply(); ok {
			data.PlayCount = *value
		}
		if value, ok := body.IsFavorite.apply(); ok {
			data.IsFavorite = *value
		}
		if value, ok := body.Likes.apply(); ok {
			if value == nil {
				data.Likes = nil
			} else {
				liked := 0
				if *value {
					liked = 1
				}
				data.Likes = &liked
			}
		}
		if value, ok := body.LastPlayedDate.apply(); ok {
			if value == nil {
				data.LastPlayed = nil
			} else {
				played := value.Time.UTC()
				data.LastPlayed = &played
			}
		}
		if value, ok := body.Played.apply(); ok {
			data.Played = *value
		}
	})
}

func (h userDataHandler) delete(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(data *store.UserData, _ userDataQuery) {
		data.PositionTicks = 0
	})
}

func (h userDataHandler) markPlayed(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(data *store.UserData, query userDataQuery) {
		if query.DatePlayed != nil {
			data.PlayCount++
		}
		if data.PlayCount < 1 {
			data.PlayCount = 1
		}
		data.PositionTicks = 0
		if query.DatePlayed != nil {
			played := query.DatePlayed.Time.UTC()
			data.LastPlayed = &played
		} else if data.LastPlayed == nil {
			played := h.clock.Now().UTC()
			data.LastPlayed = &played
		}
		data.Played = true
	})
}

func (h userDataHandler) markUnplayed(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(data *store.UserData, _ userDataQuery) {
		data.Played = false
		data.PlayCount = 0
		data.PositionTicks = 0
		data.LastPlayed = nil
	})
}

func (h userDataHandler) markFavorite(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(data *store.UserData, _ userDataQuery) {
		data.IsFavorite = true
	})
}

func (h userDataHandler) unmarkFavorite(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(data *store.UserData, _ userDataQuery) {
		data.IsFavorite = false
	})
}

func (h userDataHandler) mutate(w http.ResponseWriter, r *http.Request, change func(*store.UserData, userDataQuery)) {
	var query userDataQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		userDataBadRequest(w)
		return
	}
	itemID, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil || itemID.IsNil() {
		userDataBadRequest(w)
		return
	}
	user, ok := resolveSelectedUser(w, r, query.UserID, h.users)
	if !ok {
		return
	}
	if h.items == nil || h.mutator == nil || h.clock == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	itemQuery := store.ItemQuery{ItemIDs: []dto.UUID{itemID}}
	applyItemUser(&itemQuery, user)
	visible, err := h.items.Query(r.Context(), itemQuery)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if len(visible.Items) == 0 {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	data, err := h.mutator.MutateUserDataForItem(r.Context(), user.ID, itemID, func(data *store.UserData) error {
		change(data, query)
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || store.IsNotFound(err) {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		} else {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
		return
	}
	_ = render.JSON(w, r, http.StatusOK, dtobuild.UserItemData(data))
}

func userDataBadRequest(w http.ResponseWriter) {
	http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
}
