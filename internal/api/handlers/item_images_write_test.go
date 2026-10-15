// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/itemedit"
)

type fakeItemImageEditor struct {
	err       error
	mediaType string
	body      string
	imageType item.ImageType
	index     int
	newIndex  int
	calls     []string
}

func (f *fakeItemImageEditor) SetImage(_ context.Context, _ dto.UUID, imageType item.ImageType, mediaType string, body io.Reader) error {
	f.calls = append(f.calls, "set")
	f.imageType, f.mediaType = imageType, mediaType
	if mediaType == "" {
		return itemedit.ErrUnsupportedImage
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return itemedit.ErrInvalidRequest
	}
	f.body = string(data)
	return f.err
}

func (f *fakeItemImageEditor) DeleteImage(_ context.Context, _ dto.UUID, imageType item.ImageType, index int) error {
	f.calls = append(f.calls, "delete")
	f.imageType, f.index = imageType, index
	return f.err
}

func (f *fakeItemImageEditor) UpdateImageIndex(_ context.Context, _ dto.UUID, imageType item.ImageType, index, newIndex int) error {
	f.calls = append(f.calls, "index")
	f.imageType, f.index, f.newIndex = imageType, index, newIndex
	return f.err
}

func serveItemImageWrite(editor handlers.ItemImageEditor, method, target, contentType, body string) *httptest.ResponseRecorder {
	rt := router.New()
	handlers.RegisterItemImageWrites(rt, editor)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, request)
	return recorder
}

func TestItemImageWrites(t *testing.T) {
	base := "/Items/" + itemRefreshID + "/Images/"
	encoded := base64.StdEncoding.EncodeToString([]byte("png-bytes"))

	editor := &fakeItemImageEditor{}
	got := serveItemImageWrite(editor, http.MethodPost, base+"primary", "image/png; charset=utf-8", encoded)
	if got.Code != http.StatusNoContent || editor.body != "png-bytes" || editor.mediaType != "image/png" || editor.imageType != item.ImageTypePrimary {
		t.Fatalf("SetItemImage = %d body %q media %q type %v", got.Code, editor.body, editor.mediaType, editor.imageType)
	}
	got = serveItemImageWrite(editor, http.MethodPost, base+"Backdrop/7", "image/jpeg", encoded)
	if got.Code != http.StatusNoContent || editor.imageType != item.ImageTypeBackdrop {
		t.Fatalf("SetItemImageByIndex = %d type %v", got.Code, editor.imageType)
	}
	got = serveItemImageWrite(editor, http.MethodPost, base+"Primary", "application/json", encoded)
	if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "Incorrect ContentType.") {
		t.Fatalf("SetItemImage(bad content type) = %d %q", got.Code, got.Body.String())
	}
	if got = serveItemImageWrite(editor, http.MethodPost, base+"Bogus", "image/png", encoded); got.Code != http.StatusBadRequest {
		t.Fatalf("SetItemImage(bad type) = %d", got.Code)
	}
	if got = serveItemImageWrite(&fakeItemImageEditor{err: itemedit.ErrItemNotFound}, http.MethodPost, base+"Primary", "image/png", encoded); got.Code != http.StatusNotFound {
		t.Fatalf("SetItemImage(missing item) = %d", got.Code)
	}

	editor = &fakeItemImageEditor{}
	if got = serveItemImageWrite(editor, http.MethodDelete, base+"Backdrop?imageIndex=2", "", ""); got.Code != http.StatusNoContent || editor.index != 2 {
		t.Fatalf("DeleteItemImage = %d index %d", got.Code, editor.index)
	}
	if got = serveItemImageWrite(editor, http.MethodDelete, base+"Primary", "", ""); got.Code != http.StatusNoContent || editor.index != 0 {
		t.Fatalf("DeleteItemImage(default index) = %d index %d", got.Code, editor.index)
	}
	if got = serveItemImageWrite(editor, http.MethodDelete, base+"Backdrop/3", "", ""); got.Code != http.StatusNoContent || editor.index != 3 {
		t.Fatalf("DeleteItemImageByIndex = %d index %d", got.Code, editor.index)
	}

	if got = serveItemImageWrite(editor, http.MethodPost, base+"Backdrop/1/Index?newIndex=0", "", ""); got.Code != http.StatusNoContent || editor.index != 1 || editor.newIndex != 0 {
		t.Fatalf("UpdateItemImageIndex = %d %d→%d", got.Code, editor.index, editor.newIndex)
	}
	if got = serveItemImageWrite(editor, http.MethodPost, base+"Backdrop/1/Index", "", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("UpdateItemImageIndex(no newIndex) = %d", got.Code)
	}
	if got = serveItemImageWrite(&fakeItemImageEditor{err: itemedit.ErrInvalidRequest}, http.MethodPost, base+"Primary/0/Index?newIndex=1", "", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("UpdateItemImageIndex(Primary) = %d", got.Code)
	}
}
