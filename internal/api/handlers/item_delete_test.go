// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/itemdelete"
)

type fakeItemDeleter struct {
	err error
	ids []dto.UUID
}

func (f *fakeItemDeleter) DeleteItem(_ context.Context, _ itemdelete.Actor, id dto.UUID) error {
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeItemDeleter) DeleteItems(_ context.Context, _ itemdelete.Actor, ids []dto.UUID) error {
	f.ids = append(f.ids, ids...)
	return f.err
}

func TestItemDeleteRoutes(t *testing.T) {
	first := "0a7dc0f7-3d1c-4b2a-9e2f-3b2c1d4e5f60"
	second := "1b7dc0f7-3d1c-4b2a-9e2f-3b2c1d4e5f61"
	tests := []struct {
		name   string
		target string
		err    error
		status int
		body   string
		ids    int
	}{
		{"delete item", "/Items/" + first, nil, http.StatusNoContent, "", 1},
		{"missing item", "/Items/" + first, itemdelete.ErrNotFound, http.StatusNotFound, "", 1},
		{"unauthorized item", "/Items/" + first, itemdelete.ErrUnauthorized, http.StatusUnauthorized, "Unauthorized access", 1},
		{"bad id", "/Items/nope", nil, http.StatusBadRequest, "", 0},
		{"delete items", "/Items?ids=" + first + "," + second, nil, http.StatusNoContent, "", 2},
		{"delete no items", "/Items", nil, http.StatusNoContent, "", 0},
		{"unauthorized items", "/Items?ids=" + first, itemdelete.ErrUnauthorized, http.StatusUnauthorized, "Unauthorized access", 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deleter := &fakeItemDeleter{err: test.err}
			rt := router.New()
			RegisterItemDelete(rt, deleter)
			recorder := httptest.NewRecorder()
			rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, test.target, nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body %q", recorder.Code, test.status, recorder.Body.String())
			}
			if test.body != "" && recorder.Body.String() != test.body {
				t.Fatalf("body = %q, want %q", recorder.Body.String(), test.body)
			}
			if len(deleter.ids) != test.ids {
				t.Fatalf("ids = %v, want %d", deleter.ids, test.ids)
			}
		})
	}
}
