// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/itemedit"
)

type fakeItemEditor struct {
	err         error
	updated     *dto.ItemUpdateRequest
	contentType *string
}

func (f *fakeItemEditor) Update(_ context.Context, _ dto.UUID, request dto.ItemUpdateRequest) error {
	f.updated = &request
	return f.err
}

func (f *fakeItemEditor) MetadataEditorInfo(context.Context, dto.UUID) (dto.MetadataEditorInfo, error) {
	if f.err != nil {
		return dto.MetadataEditorInfo{}, f.err
	}
	return dto.MetadataEditorInfo{
		ParentalRatingOptions: []dto.ParentalRating{}, Countries: []dto.CountryInfo{}, Cultures: []dto.CultureDto{},
		ExternalIdInfos: []dto.ExternalIdInfo{{Name: "TheMovieDb", Key: "Tmdb"}}, ContentTypeOptions: []dto.NameValuePair{},
	}, nil
}

func (f *fakeItemEditor) UpdateContentType(_ context.Context, _ dto.UUID, contentType string) error {
	f.contentType = &contentType
	return f.err
}

func serveItemUpdate(editor handlers.ItemEditor, method, target, contentType, body string) *httptest.ResponseRecorder {
	rt := router.New()
	handlers.RegisterItemUpdate(rt, editor)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, request)
	return recorder
}

func TestItemUpdateStatusCodes(t *testing.T) {
	path := "/Items/" + itemRefreshID
	for _, test := range []struct {
		name        string
		err         error
		contentType string
		body        string
		want        int
	}{
		{name: "updated", contentType: "application/json", body: `{"Name":"x","ProviderIds":{"Tmdb":"1"}}`, want: http.StatusNoContent},
		{name: "text json alias", contentType: "text/json", body: `{"Name":"x"}`, want: http.StatusNoContent},
		{name: "missing item", err: itemedit.ErrItemNotFound, contentType: "application/json", body: `{}`, want: http.StatusNotFound},
		{name: "unknown enum", err: itemedit.ErrInvalidRequest, contentType: "application/json", body: `{}`, want: http.StatusBadRequest},
		{name: "empty body", contentType: "application/json", want: http.StatusBadRequest},
		{name: "malformed body", contentType: "application/json", body: `{`, want: http.StatusBadRequest},
		{name: "unsupported media type", contentType: "text/plain", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "store failure", err: fmt.Errorf("disk"), contentType: "application/json", body: `{}`, want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := &fakeItemEditor{err: test.err}
			got := serveItemUpdate(editor, http.MethodPost, path, test.contentType, test.body)
			if got.Code != test.want {
				t.Fatalf("status = %d, want %d (%s)", got.Code, test.want, got.Body.String())
			}
		})
	}

	editor := &fakeItemEditor{}
	serveItemUpdate(editor, http.MethodPost, path, "application/json", `{"name":"lower case","ProviderIds":{"Tvdb":"2","Tmdb":"1"}}`)
	if editor.updated == nil || editor.updated.Name == nil || *editor.updated.Name != "lower case" {
		t.Fatalf("bound request = %+v; want case-insensitive Name", editor.updated)
	}
	if ids := editor.updated.ProviderIDs; len(ids) != 2 || ids[0].Name != "Tvdb" || ids[1].Name != "Tmdb" {
		t.Fatalf("ProviderIds = %+v; want document order", ids)
	}
}

func TestItemMetadataEditorAndContentType(t *testing.T) {
	editor := &fakeItemEditor{}
	got := serveItemUpdate(editor, http.MethodGet, "/Items/"+itemRefreshID+"/MetadataEditor", "", "")
	if got.Code != http.StatusOK {
		t.Fatalf("MetadataEditor status = %d", got.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode editor info: %v", err)
	}
	for _, key := range []string{"ParentalRatingOptions", "Countries", "Cultures", "ExternalIdInfos", "ContentTypeOptions"} {
		if _, found := body[key]; !found {
			t.Fatalf("editor info lacks %s: %s", key, got.Body.String())
		}
	}
	if _, found := body["ContentType"]; found {
		t.Fatalf("null ContentType was written: %s", got.Body.String())
	}

	missing := serveItemUpdate(&fakeItemEditor{err: itemedit.ErrItemNotFound}, http.MethodGet, "/Items/"+itemRefreshID+"/MetadataEditor", "", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("MetadataEditor(missing) status = %d, want 404", missing.Code)
	}

	got = serveItemUpdate(editor, http.MethodPost, "/Items/"+itemRefreshID+"/ContentType?contentType=movies", "", "")
	if got.Code != http.StatusNoContent || editor.contentType == nil || *editor.contentType != "movies" {
		t.Fatalf("ContentType status = %d value %v", got.Code, editor.contentType)
	}
}
