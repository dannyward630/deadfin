// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationUserViewsUseDetailedSourceProjection exercises the composed,
// persisted topology and item store. It guards the source distinction between
// UserViews (source-shaped UserView DTOs) and GroupingOptions (concrete
// CollectionFolder identities): no route-local sparse DTO shortcut may erase
// the view's user data, default collections, parent/path, or child counts.
func TestApplicationUserViewsUseDetailedSourceProjection(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	admin := configureUserViewsCompositionAdministrator(t, app)
	fixture := seedComposedUserViews(t, app, admin.ID)
	token := loginComposedAdministrator(t, app.Router())

	modern := composedUserViews(t, app, token, "/UserViews?PresetViews=Music")
	legacy := composedUserViews(t, app, token, "/Users/"+admin.ID.Dashed()+"/Views?PresetViews=Music")
	if !reflect.DeepEqual(modern, legacy) {
		t.Fatalf("modern and legacy UserViews differ:\nmodern=%#v\nlegacy=%#v", modern, legacy)
	}

	if got, want := userViewNames(modern.Items), []string{"Golden Music", "Golden Photos", "Movies", "TV Shows"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UserViews names = %#v, want ordered %#v", got, want)
	}
	if modern.TotalRecordCount != 4 || modern.StartIndex != 0 {
		t.Fatalf("UserViews page = %#v, want four unpaged views", modern)
	}

	byName := userViewsByName(t, modern.Items)
	for name, collectionType := range map[string]string{
		"Golden Music":  "music",
		"Golden Photos": "homevideos",
		"Movies":        "movies",
		"TV Shows":      "tvshows",
	} {
		view := byName[name]
		wantType := "UserView"
		if name == "Golden Photos" {
			wantType = "CollectionFolder"
		}
		if got := composedUserViewString(t, view, "Type"); got != wantType {
			t.Fatalf("%s Type = %q, want %s", name, got, wantType)
		}
		if got := composedUserViewString(t, view, "CollectionType"); got != collectionType {
			t.Fatalf("%s CollectionType = %q, want %q", name, got, collectionType)
		}
		if got := composedUserViewString(t, view, "DisplayPreferencesId"); got == "" {
			t.Fatalf("%s omitted per-view DisplayPreferencesId", name)
		}
		assertComposedUserViewDefaults(t, name, view)
		assertComposedUserViewUserData(t, name, view)
	}

	photos := byName["Golden Photos"]
	if got := composedUserViewString(t, photos, "Id"); got != fixture.photos.ID.String() {
		t.Fatalf("concrete photo UserView ID = %q, want persisted collection folder %s", got, fixture.photos.ID)
	}
	if got := composedUserViewString(t, photos, "DisplayPreferencesId"); got != fixture.photos.ID.String() {
		t.Fatalf("concrete photo DisplayPreferencesId = %q, want row identity %s", got, fixture.photos.ID)
	}
	if got := composedUserViewString(t, photos, "ParentId"); got != fixture.root.ID.String() {
		t.Fatalf("concrete photo UserView ParentId = %q, want root %s", got, fixture.root.ID)
	}
	if got := composedUserViewString(t, photos, "Path"); got != *fixture.photos.Path {
		t.Fatalf("concrete photo UserView Path = %q, want %q", got, *fixture.photos.Path)
	}
	childCount := composedUserViewNumber(t, photos, "ChildCount")
	if childCount < 1 || childCount > 9 {
		t.Fatalf("concrete photo UserView ChildCount = %d, want upstream-valid sentinel in [1, 9]", childCount)
	}
	repeated := composedUserViews(t, app, token, "/UserViews?PresetViews=Music")
	repeatedPhotos := userViewsByName(t, repeated.Items)["Golden Photos"]
	if got := composedUserViewString(t, repeatedPhotos, "Id"); got != fixture.photos.ID.String() {
		t.Fatalf("repeated concrete photo UserView ID = %q, want persisted collection folder %s", got, fixture.photos.ID)
	}
	if got := composedUserViewNumber(t, repeatedPhotos, "ChildCount"); got != childCount {
		t.Fatalf("same concrete photo UserView ChildCount changed from %d to %d", childCount, got)
	}
	if got := composedUserViewString(t, byName["Golden Music"], "Id"); got == fixture.music.ID.String() {
		t.Fatalf("preset music UserView reused concrete CollectionFolder ID %s", fixture.music.ID)
	}
	for _, name := range []string{"Movies", "TV Shows"} {
		view := byName[name]
		if got := composedUserViewString(t, view, "ParentId"); got != fixture.root.ID.String() {
			t.Fatalf("generated %s ParentId = %q, want root %s", name, got, fixture.root.ID)
		}
		if got, want := composedUserViewString(t, view, "Path"), filepath.Dir(*fixture.movieA.Path); got != want {
			t.Fatalf("generated %s Path = %q, want common source-root path %q", name, got, want)
		}
	}
	if _, found := byName["Excluded Music"]; found {
		t.Fatalf("excluded library leaked into UserViews: %#v", modern.Items)
	}

	options := composedGroupingOptions(t, app, token, "/UserViews/GroupingOptions")
	legacyOptions := composedGroupingOptions(t, app, token, "/Users/"+admin.ID.Dashed()+"/GroupingOptions")
	if !reflect.DeepEqual(options, legacyOptions) {
		t.Fatalf("modern and legacy GroupingOptions differ:\nmodern=%#v\nlegacy=%#v", options, legacyOptions)
	}
	wantOptions := map[string]string{
		"Alpha Movies": fixture.movieA.ID.String(),
		"Alpha Shows":  fixture.showA.ID.String(),
		"Zeta Movies":  fixture.movieB.ID.String(),
		"Zeta Shows":   fixture.showB.ID.String(),
	}
	if got := groupingOptionIDs(t, options); !reflect.DeepEqual(got, wantOptions) {
		t.Fatalf("GroupingOptions = %#v, want concrete movie/TV collection IDs %#v", got, wantOptions)
	}
	for _, optionID := range wantOptions {
		if optionID == "" {
			t.Fatal("GroupingOptions emitted an empty concrete CollectionFolder identity")
		}
	}
}

type composedUserViewsResponse struct {
	Items            []map[string]any `json:"Items"`
	TotalRecordCount int              `json:"TotalRecordCount"`
	StartIndex       int              `json:"StartIndex"`
}

type composedUserViewsFixture struct {
	root, movieA, movieB, showA, showB, music, photos item.Item
}

func configureUserViewsCompositionAdministrator(t *testing.T, app *application) store.User {
	t.Helper()
	configureComposedAdministrator(t, app.Router())
	admin, err := app.UserService().GetByName(t.Context(), "captain")
	if err != nil {
		t.Fatalf("load composed administrator: %v", err)
	}
	return admin
}

func seedComposedUserViews(t *testing.T, app *application, userID dto.UUID) composedUserViewsFixture {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "view-root")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatalf("make view root: %v", err)
	}
	root := composedUserViewItem(t, 0x9000, item.KindFolder, nil, "View Root", rootPath)
	root.IsFolder = true
	items := store.NewItemStore(app.stores)
	if err := items.Upsert(t.Context(), root); err != nil {
		t.Fatalf("upsert UserViews root: %v", err)
	}

	addFolder := func(name, collection string, id int) (library.VirtualFolder, item.Item) {
		t.Helper()
		source := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatalf("create %s source: %v", name, err)
		}
		folder, err := app.topology.AddVirtualFolder(t.Context(), name, collection, library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}})
		if err != nil {
			t.Fatalf("add %s virtual folder: %v", name, err)
		}
		value := composedUserViewItem(t, id, item.KindCollectionFolder, &root.ID, name, folder.Path)
		value.ID, value.IsFolder = folder.ID, true
		collectionType := composedUserViewCollectionType(t, collection)
		value.Extra.CollectionFolder = &item.CollectionFolderExtra{CollectionType: &collectionType}
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s CollectionFolder: %v", name, err)
		}
		return folder, value
	}

	_, movieA := addFolder("Alpha Movies", "movies", 0x9001)
	_, movieB := addFolder("Zeta Movies", "movies", 0x9002)
	_, showA := addFolder("Alpha Shows", "tvshows", 0x9003)
	_, showB := addFolder("Zeta Shows", "tvshows", 0x9004)
	_, music := addFolder("Golden Music", "music", 0x9005)
	_, photos := addFolder("Golden Photos", "homevideos", 0x9006)
	_, excluded := addFolder("Excluded Music", "music", 0x9007)
	parents := []item.Item{movieA, movieA, movieB, showA, showB, music, photos, excluded}
	for index, parent := range parents {
		child := composedUserViewItem(t, 0x9100+index, item.KindMovie, &parent.ID, "child", filepath.Join(rootPath, "child"))
		if err := items.Upsert(t.Context(), child); err != nil {
			t.Fatalf("upsert child of %s: %v", *parent.Name, err)
		}
	}

	admin, err := app.UserService().Get(t.Context(), userID)
	if err != nil {
		t.Fatalf("load UserViews user: %v", err)
	}
	configuration, err := json.Marshal(map[string]any{
		"GroupedFolders":  []dto.UUID{movieA.ID, movieB.ID, showA.ID, showB.ID},
		"MyMediaExcludes": []dto.UUID{excluded.ID},
		"OrderedViews":    []dto.UUID{music.ID, photos.ID},
	})
	if err != nil {
		t.Fatalf("marshal UserViews configuration: %v", err)
	}
	admin.Configuration = configuration
	if err := app.UserService().Update(t.Context(), admin); err != nil {
		t.Fatalf("persist UserViews configuration: %v", err)
	}
	return composedUserViewsFixture{root: root, movieA: movieA, movieB: movieB, showA: showA, showB: showB, music: music, photos: photos}
}

func composedUserViewItem(t *testing.T, value int, kind item.Kind, parent *dto.UUID, name, path string) item.Item {
	t.Helper()
	id, err := dto.ParseUUID("00000000-0000-0000-0000-" + composedUserViewHex(t, value))
	if err != nil {
		t.Fatalf("parse composed UserViews item ID: %v", err)
	}
	return item.Item{ID: id, Kind: kind, ParentID: parent, Name: &name, SortName: &name, Path: &path}
}

func composedUserViewHex(t *testing.T, value int) string {
	t.Helper()
	return fmt.Sprintf("%012x", value)
}

func composedUserViewCollectionType(t *testing.T, value string) item.CollectionType {
	t.Helper()
	switch value {
	case "movies":
		return item.CollectionTypeMovies
	case "tvshows":
		return item.CollectionTypeTVShows
	case "music":
		return item.CollectionTypeMusic
	case "homevideos":
		return item.CollectionTypeHomeVideos
	default:
		t.Fatalf("unsupported composed UserViews collection type %q", value)
		return item.CollectionTypeUnknown
	}
}

func composedUserViews(t *testing.T, app *application, token, path string) composedUserViewsResponse {
	t.Helper()
	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, record.Code, record.Body.String())
	}
	var response composedUserViewsResponse
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s: %v; body=%s", path, err, record.Body.String())
	}
	return response
}

func composedGroupingOptions(t *testing.T, app *application, token, path string) []map[string]any {
	t.Helper()
	record := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, record.Code, record.Body.String())
	}
	var options []map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &options); err != nil {
		t.Fatalf("decode %s: %v; body=%s", path, err, record.Body.String())
	}
	return options
}

func userViewNames(items []map[string]any) []string {
	names := make([]string, len(items))
	for index, view := range items {
		names[index], _ = view["Name"].(string)
	}
	return names
}

func userViewsByName(t *testing.T, items []map[string]any) map[string]map[string]any {
	t.Helper()
	byName := make(map[string]map[string]any, len(items))
	for _, view := range items {
		name := composedUserViewString(t, view, "Name")
		byName[name] = view
	}
	return byName
}

func groupingOptionIDs(t *testing.T, options []map[string]any) map[string]string {
	t.Helper()
	byName := make(map[string]string, len(options))
	for _, option := range options {
		byName[composedUserViewString(t, option, "Name")] = composedUserViewString(t, option, "Id")
	}
	return byName
}

func assertComposedUserViewDefaults(t *testing.T, name string, view map[string]any) {
	t.Helper()
	for field, want := range map[string]any{
		"CanDelete":                false,
		"CanDownload":              false,
		"EnableMediaSourceDisplay": true,
		"IsFolder":                 true,
		"LockData":                 false,
		"LocationType":             "FileSystem",
		"MediaType":                "Unknown",
		"PlayAccess":               "Full",
	} {
		if got, found := view[field]; !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s default %s = %#v (present=%t), want %#v", name, field, got, found, want)
		}
	}
	for _, field := range []string{"BackdropImageTags", "Chapters", "ExternalUrls", "GenreItems", "Genres", "LockedFields", "People", "RemoteTrailers", "Studios", "Taglines", "Tags"} {
		value, found := view[field]
		if !found || reflect.ValueOf(value).Kind() != reflect.Slice || reflect.ValueOf(value).Len() != 0 {
			t.Fatalf("%s default %s = %#v (present=%t), want allocated empty array", name, field, value, found)
		}
	}
	for _, field := range []string{"ImageBlurHashes", "ImageTags", "ProviderIds"} {
		value, found := view[field]
		if !found || reflect.ValueOf(value).Kind() != reflect.Map || reflect.ValueOf(value).Len() != 0 {
			t.Fatalf("%s default %s = %#v (present=%t), want allocated empty object", name, field, value, found)
		}
	}
}

func assertComposedUserViewUserData(t *testing.T, name string, view map[string]any) {
	t.Helper()
	data, found := view["UserData"].(map[string]any)
	if !found {
		t.Fatalf("%s UserData = %#v, want populated default user data", name, view["UserData"])
	}
	if got, want := composedUserViewString(t, data, "ItemId"), composedUserViewString(t, view, "Id"); got != want {
		t.Fatalf("%s UserData.ItemId = %q, want view ID %q", name, got, want)
	}
	for field, want := range map[string]any{"IsFavorite": false, "Played": false, "PlayCount": float64(0), "PlaybackPositionTicks": float64(0)} {
		if got, found := data[field]; !found || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s UserData.%s = %#v (present=%t), want %#v", name, field, got, found, want)
		}
	}
}

func composedUserViewString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	text, found := value[key].(string)
	if !found {
		t.Fatalf("field %s = %#v, want string", key, value[key])
	}
	return text
}

func composedUserViewNumber(t *testing.T, value map[string]any, key string) int {
	t.Helper()
	number, found := value[key].(float64)
	if !found || number != float64(int(number)) {
		t.Fatalf("field %s = %#v, want integer", key, value[key])
	}
	return int(number)
}
