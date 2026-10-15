// SPDX-License-Identifier: GPL-2.0-only

package bind

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// sortOrder is a stand-in enum with the case-insensitive TextUnmarshaler our
// real dto enums implement.
type sortOrder string

func (s *sortOrder) UnmarshalText(text []byte) error {
	switch strings.ToLower(string(text)) {
	case "ascending":
		*s = "Ascending"
	case "descending":
		*s = "Descending"
	default:
		return errBadEnum
	}
	return nil
}

var errBadEnum = &badEnum{}

type badEnum struct{}

func (*badEnum) Error() string { return "bad enum" }

type itemsParams struct {
	SortBy          []string    `query:"sortBy"`
	SortOrder       []sortOrder `query:"sortOrder"`
	Ids             []dto.UUID  `query:"ids"`
	StartIndex      *int        `query:"startIndex"`
	Limit           *int        `query:"limit"`
	Recursive       *bool       `query:"recursive"`
	SearchTerm      string      `query:"searchTerm"`
	MaxWidth        int         `query:"maxWidth"`
	CommunityRating *float64    `query:"minCommunityRating"`
	EnableUserData  *bool       `query:"enableUserData"`
	MinPremiereDate *dto.Time   `query:"minPremiereDate"`
	Untouched       string      `query:"untouched"`
}

func TestQuery_CSVAndRepeatedAreEquivalent(t *testing.T) {
	csv := url.Values{"sortBy": {"SortName,ProductionYear"}}
	repeated := url.Values{"sortBy": {"SortName", "ProductionYear"}}
	mixed := url.Values{"sortBy": {"SortName,ProductionYear", "DateCreated"}}

	var a, b, c itemsParams
	for dst, src := range map[*itemsParams]url.Values{&a: csv, &b: repeated, &c: mixed} {
		if err := Query(src, dst); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.SortBy) != 2 || a.SortBy[0] != "SortName" || a.SortBy[1] != "ProductionYear" {
		t.Errorf("csv: %v", a.SortBy)
	}
	if len(b.SortBy) != 2 || b.SortBy[0] != "SortName" || b.SortBy[1] != "ProductionYear" {
		t.Errorf("repeated: %v", b.SortBy)
	}
	if len(c.SortBy) != 3 || c.SortBy[2] != "DateCreated" {
		t.Errorf("mixed: %v", c.SortBy)
	}
}

func TestQuery_CaseInsensitiveKeysAndEnumValues(t *testing.T) {
	src := url.Values{
		"SORTORDER":  {"descending"}, // key case + enum value case both forgiven
		"StartIndex": {"40"},
		"RECURSIVE":  {"True"},
	}
	var p itemsParams
	if err := Query(src, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.SortOrder) != 1 || p.SortOrder[0] != "Descending" {
		t.Errorf("sortOrder: %v", p.SortOrder)
	}
	if p.StartIndex == nil || *p.StartIndex != 40 {
		t.Errorf("startIndex: %v", p.StartIndex)
	}
	if p.Recursive == nil || !*p.Recursive {
		t.Errorf("recursive: %v", p.Recursive)
	}
}

// Case-folding must not turn scalar selection into a map-order-dependent
// privilege decision. Distinct spellings may repeat an identical value, but
// different non-empty scalar values must be rejected before any caller uses
// one of them.
func TestQueryRejectsConflictingCaseFoldedScalarAliases(t *testing.T) {
	type playbackParams struct {
		UserID              *dto.UUID `query:"userId"`
		MediaSourceID       *string   `query:"mediaSourceId"`
		AudioStreamIndex    *int      `query:"audioStreamIndex"`
		SubtitleStreamIndex *int      `query:"subtitleStreamIndex"`
	}

	first := itemsBindUUID(t, 610)
	second := itemsBindUUID(t, 611)
	for _, tc := range []struct {
		name   string
		values url.Values
	}{
		{name: "user ID", values: url.Values{"userId": {first.Dashed()}, "USERID": {second.Dashed()}}},
		{name: "media source ID", values: url.Values{"mediaSourceId": {"source-a"}, "MEDIASOURCEID": {"source-b"}}},
		{name: "audio stream index", values: url.Values{"audioStreamIndex": {"1"}, "AUDIOSTREAMINDEX": {"2"}}},
		{name: "subtitle stream index", values: url.Values{"subtitleStreamIndex": {"3"}, "SUBTITLESTREAMINDEX": {"4"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got playbackParams
			if err := Query(tc.values, &got); err == nil {
				t.Fatalf("Query(%v) accepted conflicting case-folded scalar aliases: %#v", tc.values, got)
			}
		})
	}
}

func TestQueryAcceptsIdenticalCaseFoldedScalarAliasesWithoutChangingSliceControls(t *testing.T) {
	type params struct {
		UserID *dto.UUID `query:"userId"`
		Fields []string  `query:"fields"`
	}

	userID := itemsBindUUID(t, 612)
	values := url.Values{
		"UserId": {userID.Dashed()},
		"USERID": {userID.Dashed()},
		"fields": {"MediaSources,ParentId", "People", ""},
	}
	var got params
	if err := Query(values, &got); err != nil {
		t.Fatalf("Query(%v): %v", values, err)
	}
	if got.UserID == nil || *got.UserID != userID {
		t.Fatalf("UserID = %v, want repeated alias value %s", got.UserID, userID)
	}
	if want := []string{"MediaSources", "ParentId", "People"}; !reflect.DeepEqual(got.Fields, want) {
		t.Fatalf("Fields = %v, want CSV/repeated values %v", got.Fields, want)
	}
}

func TestQuery_UUIDListWithEmptiesAndLimit40Shape(t *testing.T) {
	// jellyfin-web's batcher sends up to 40 GUIDs as one CSV; empty entries
	// from ",x," shapes are dropped.
	ids := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		ids = append(ids, "8a1b4a2f9dbe4f379f6b2b1c3d4e5f60")
	}
	src := url.Values{"ids": {"," + strings.Join(ids, ",") + ","}}
	var p itemsParams
	if err := Query(src, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Ids) != 40 {
		t.Errorf("ids: got %d, want 40", len(p.Ids))
	}
	// Dashed GUIDs bind too.
	src = url.Values{"ids": {"8A1B4A2F-9DBE-4F37-9F6B-2B1C3D4E5F60"}}
	p = itemsParams{}
	if err := Query(src, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Ids) != 1 || p.Ids[0].String() != "8a1b4a2f9dbe4f379f6b2b1c3d4e5f60" {
		t.Errorf("dashed id: %v", p.Ids)
	}
}

func TestQuery_EmptyValueBindsNothing(t *testing.T) {
	src := url.Values{"limit": {""}, "searchTerm": {""}}
	var p itemsParams
	if err := Query(src, &p); err != nil {
		t.Fatal(err)
	}
	if p.Limit != nil {
		t.Errorf("empty limit must stay nil, got %v", *p.Limit)
	}
	if p.SearchTerm != "" {
		t.Errorf("searchTerm: %q", p.SearchTerm)
	}
}

func TestQueryRequestMatchesQueryURLSemantics(t *testing.T) {
	values := url.Values{
		"sortBy":     {"SortName,ProductionYear", "", "DateCreated"},
		"limit":      {""},
		"searchTerm": {""},
	}
	request := httptest.NewRequest(http.MethodGet, "/?"+values.Encode(), nil)

	var want, got itemsParams
	if err := Query(request.URL.Query(), &want); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if err := QueryRequest(request, &got); err != nil {
		t.Fatalf("QueryRequest: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("QueryRequest result = %#v, want Query result %#v", got, want)
	}
}

func TestQuery_Errors(t *testing.T) {
	if err := Query(url.Values{"limit": {"abc"}}, &itemsParams{}); err == nil {
		t.Error("bad int must error")
	}
	if err := Query(url.Values{"recursive": {"1"}}, &itemsParams{}); err == nil {
		t.Error(`bool "1" must error (bool.TryParse semantics)`)
	}
	if err := Query(url.Values{"sortOrder": {"sideways"}}, &itemsParams{}); err == nil {
		t.Error("bad enum must error")
	}
}

func TestQuery_TimeBinding(t *testing.T) {
	var p itemsParams
	if err := Query(url.Values{"minPremiereDate": {"2024-01-02T03:04:05Z"}}, &p); err != nil {
		t.Fatal(err)
	}
	if p.MinPremiereDate == nil || p.MinPremiereDate.Year() != 2024 {
		t.Errorf("minPremiereDate: %v", p.MinPremiereDate)
	}
}

func TestJSONBody_ContentTypeAliases(t *testing.T) {
	type payload struct {
		DeviceProfile *struct {
			Name string `json:"Name"`
		} `json:"DeviceProfile"`
	}
	body := `{"deviceProfile":{"name":"iOS"}}` // camelCase in: case-insensitive match

	for _, ct := range []string{
		"application/json",
		"text/json",
		"application/playbackinfo+json",
		"application/json; charset=utf-8",
		"", // absent content type still parses
	} {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		var p payload
		if err := JSONBody(req, &p); err != nil {
			t.Fatalf("ct=%q: %v", ct, err)
		}
		if p.DeviceProfile == nil || p.DeviceProfile.Name != "iOS" {
			t.Errorf("ct=%q: %+v", ct, p)
		}
	}

	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	if err := JSONBody(req, &struct{}{}); err != ErrUnsupportedMediaType {
		t.Errorf("text/plain: got %v", err)
	}

	// Empty body binds nothing.
	req = httptest.NewRequest("POST", "/x", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	var p payload
	if err := JSONBody(req, &p); err != nil || p.DeviceProfile != nil {
		t.Errorf("empty body: %v %+v", err, p)
	}
}

func TestImageBody_NormalizesImageContentType(t *testing.T) {
	want := []byte{0x00, 0xff, 0x10, 'J', 'F', 'I', 'F'}
	encoded := base64.StdEncoding.EncodeToString(want)

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "APNG", input: "image/apng", want: "image/apng"},
		{name: "AVIF", input: "image/avif", want: "image/avif"},
		{name: "BMP", input: "image/bmp", want: "image/bmp"},
		{name: "GIF", input: "image/gif", want: "image/gif"},
		{name: "X-Icon", input: "image/x-icon", want: "image/x-icon"},
		{name: "JPEG", input: "image/jpeg", want: "image/jpeg"},
		{name: "JPG alias", input: "image/jpg", want: "image/jpg"},
		{name: "PNG with parameter", input: "image/png; charset=utf-8", want: "image/png"},
		{name: "X-PNG alias", input: "image/x-png", want: "image/x-png"},
		{name: "SVG+XML", input: "image/svg+xml", want: "image/svg+xml"},
		{name: "TIFF", input: "image/tiff", want: "image/tiff"},
		{name: "WebP", input: "image/webp", want: "image/webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/images", strings.NewReader(encoded))
			req.Header.Set("Content-Type", tc.input)

			body, mediaType, err := ImageBody(req)
			if err != nil {
				t.Fatalf("ImageBody() error: %v", err)
			}
			if mediaType != tc.want {
				t.Errorf("media type = %q, want %q", mediaType, tc.want)
			}
			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("read decoded image: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("decoded image = %x, want %x", got, want)
			}
		})
	}
}

func TestImageBody_StreamsExactBytesWithoutEagerReadOrClose(t *testing.T) {
	want := []byte{0x00, 0xff, 0x01, 0x02, 's', 't', 'r', 'e', 'a', 'm', 0x80}
	requestBody := &countingReadCloser{reader: strings.NewReader(base64.StdEncoding.EncodeToString(want))}
	req := httptest.NewRequest(http.MethodPost, "/images", nil)
	req.Header.Set("Content-Type", "image/png")
	req.Body = requestBody

	decoded, _, err := ImageBody(req)
	if err != nil {
		t.Fatalf("ImageBody() error: %v", err)
	}
	if requestBody.reads != 0 {
		t.Fatalf("ImageBody() read request body %d times before its reader was consumed", requestBody.reads)
	}
	if requestBody.closes != 0 {
		t.Fatalf("ImageBody() closed the request body")
	}

	var got bytes.Buffer
	var chunk [2]byte
	for {
		n, readErr := decoded.Read(chunk[:])
		got.Write(chunk[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read decoded image: %v", readErr)
		}
		if n == 0 {
			t.Fatal("decoded reader returned no bytes and no error")
		}
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("chunked decoded image = %x, want %x", got.Bytes(), want)
	}
}

func TestImageBody_IgnoresDotNetBase64Whitespace(t *testing.T) {
	want := []byte("white space survives")
	encoded := base64.StdEncoding.EncodeToString(want)
	var body strings.Builder
	whitespace := " \t\r\n"
	for i := range encoded {
		body.WriteByte(encoded[i])
		body.WriteByte(whitespace[i%len(whitespace)])
	}

	req := httptest.NewRequest(http.MethodPost, "/images", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "image/gif")
	decoded, _, err := ImageBody(req)
	if err != nil {
		t.Fatalf("ImageBody() error: %v", err)
	}
	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("read whitespace-separated Base64: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded whitespace-separated image = %q, want %q", got, want)
	}
}

func TestImageBody_MalformedBase64FailsWhenRead(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/images", strings.NewReader("aGVsbG8=%%%"))
	req.Header.Set("Content-Type", "image/png")

	decoded, mediaType, err := ImageBody(req)
	if err != nil {
		t.Fatalf("ImageBody() error: %v", err)
	}
	if mediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", mediaType)
	}
	got, readErr := io.ReadAll(decoded)
	if string(got) != "hello" {
		t.Errorf("decoded prefix = %q, want hello", got)
	}
	if readErr == nil {
		t.Fatal("malformed Base64 did not return a read error")
	}
}

func TestImageBody_RejectsInvalidContentType(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
	}{
		{name: "missing"},
		{name: "malformed", contentType: "image/png; charset"},
		{name: "non-image", contentType: "application/octet-stream"},
		{name: "unregistered image subtype", contentType: "image/vnd.example+json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/images", strings.NewReader("aGVsbG8="))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}

			_, _, err := ImageBody(req)
			if err != ErrUnsupportedMediaType {
				t.Errorf("ImageBody() error = %v, want ErrUnsupportedMediaType", err)
			}
		})
	}
}

type countingReadCloser struct {
	reader io.Reader
	reads  int
	closes int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

func (r *countingReadCloser) Close() error {
	r.closes++
	return nil
}
