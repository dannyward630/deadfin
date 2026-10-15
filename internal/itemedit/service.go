// SPDX-License-Identifier: GPL-2.0-only

// Package itemedit ports the client metadata editor: ItemUpdateController
// (UpdateItem, GetMetadataEditorInfo, UpdateItemContentType). It loads the
// stored item, applies the edit with upstream semantics, and persists the
// result through the item store's edit write.
package itemedit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/localization"
	"github.com/deadfin-org/deadfin/internal/path"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ErrItemNotFound reports that the edited item does not exist.
var ErrItemNotFound = errors.New("itemedit: item not found")

// ErrInvalidRequest reports an update body with a value upstream cannot bind,
// such as an unknown LockedFields or Video3DFormat name.
var ErrInvalidRequest = errors.New("itemedit: invalid request")

// Items is the item persistence seam. *store.ItemStore satisfies it.
type Items interface {
	GetBatch(context.Context, []dto.UUID) ([]item.Item, error)
	Query(context.Context, store.ItemQuery) (store.ItemQueryResult, error)
	EditItems(ctx context.Context, edited []item.Item, entities []item.Item) error
}

// Libraries lists the configured libraries. *library.Topology satisfies it.
type Libraries interface {
	VirtualFolders(context.Context) ([]library.VirtualFolder, error)
}

// Publisher receives item-updated notifications. *library.ChangeBus
// satisfies it.
type Publisher interface {
	PublishItemUpdated(parentID, collectionFolderID dto.UUID, ids []dto.UUID)
}

// Options constructs a Service. Libraries, Config, Localization, and
// Publisher may be nil; the service then skips the dependent behavior.
type Options struct {
	Items        Items
	Libraries    Libraries
	Config       *config.Manager
	Localization *localization.Service
	Publisher    Publisher
	Clock        clock.Clock
	// MetadataPath is the internal metadata root (config.Paths.Metadata).
	// Image uploads need it.
	MetadataPath string
	// Paths expands stored virtual image paths.
	Paths path.VirtualPathExpander
}

// Service applies client metadata edits.
type Service struct {
	items        Items
	libraries    Libraries
	config       *config.Manager
	localization *localization.Service
	publisher    Publisher
	clock        clock.Clock
	metadataPath string
	paths        path.VirtualPathExpander
}

// New constructs the edit service. Items and Clock are required.
func New(options Options) *Service {
	if options.Items == nil {
		panic("itemedit: items are required")
	}
	if options.Clock == nil {
		panic("itemedit: clock is required")
	}
	return &Service{
		items:        options.Items,
		libraries:    options.Libraries,
		config:       options.Config,
		localization: options.Localization,
		publisher:    options.Publisher,
		clock:        options.Clock,
		metadataPath: options.MetadataPath,
		paths:        options.Paths,
	}
}

func (s *Service) load(ctx context.Context, id dto.UUID) (item.Item, error) {
	loaded, err := s.items.GetBatch(ctx, []dto.UUID{id})
	if err != nil {
		return item.Item{}, err
	}
	if len(loaded) == 0 {
		return item.Item{}, ErrItemNotFound
	}
	return loaded[0], nil
}

// Update applies a metadata edit (ItemUpdateController.cs:72-131). The edited
// item, the children the edit cascades to, and the entities the new values
// reference persist in one write.
func (s *Service) Update(ctx context.Context, id dto.UUID, request dto.ItemUpdateRequest) error {
	target, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	parsed, err := parseRequest(request)
	if err != nil {
		return err
	}

	newLockData := request.LockData != nil && *request.LockData
	lockChanged := target.IsLocked != newLockData

	// UpdatePeople runs before the field update (ItemUpdateController.cs:94-103).
	if request.People != nil {
		target.People = editedPeople(request.People, parsed.peopleKinds)
	}

	changes := newChangeSet()
	if err := s.cascade(ctx, &target, request, changes); err != nil {
		return err
	}
	applyFields(&target, request, parsed)
	refreshSortName(&target)

	// A changed item lock propagates to every descendant
	// (ItemUpdateController.cs:113-122).
	if lockChanged && target.IsFolder {
		descendants, err := s.children(ctx, target.ID, true)
		if err != nil {
			return err
		}
		for _, child := range descendants {
			edited := changes.get(child)
			edited.IsLocked = newLockData
			changes.put(edited)
		}
	}

	now := s.clock.Now().UTC()
	target.DateLastSaved = &now
	edited := []item.Item{target}
	for _, child := range changes.ordered() {
		child.DateLastSaved = &now
		edited = append(edited, child)
	}

	collection, err := s.collectionType(ctx, target)
	if err != nil {
		return err
	}
	entities := library.ReferencedEntities(collection, []item.Item{target})
	if err := s.items.EditItems(ctx, edited, entities); err != nil {
		if errors.Is(err, store.ErrEditItemNotFound) {
			return ErrItemNotFound
		}
		return err
	}
	s.publish(edited)
	return nil
}

// cascade applies the series, season, and album child updates
// (ItemUpdateController.cs:309-402). It reads the request's official rating
// after upstream blanks a whitespace value.
func (s *Service) cascade(ctx context.Context, target *item.Item, request dto.ItemUpdateRequest, changes *changeSet) error {
	if target.Kind != item.KindSeries && target.Kind != item.KindSeason && target.Kind != item.KindMusicAlbum {
		return nil
	}
	officialRating := blankToNil(request.OfficialRating)
	customRating := cloneString(request.CustomRating)
	var added, removed []string
	if request.Tags != nil {
		newTags := distinctFold(trimAll(request.Tags))
		removed = exceptOrdinal(target.Values.Tags, newTags)
		added = exceptOrdinal(newTags, target.Values.Tags)
	}
	applyChild := func(child *item.Item) {
		if !hasLockedField(child.LockedFields, item.MetadataFieldOfficialRating) {
			child.OfficialRating = cloneString(officialRating)
		}
		child.CustomRating = cloneString(customRating)
		if !hasLockedField(child.LockedFields, item.MetadataFieldTags) {
			child.Values.Tags = distinctFold(exceptOrdinal(append(append([]string(nil), child.Values.Tags...), added...), removed))
		}
	}

	children, err := s.children(ctx, target.ID, false)
	if err != nil {
		return err
	}
	switch target.Kind {
	case item.KindSeries:
		seriesName := cloneString(request.Name)
		for _, season := range children {
			if season.Kind != item.KindSeason {
				continue
			}
			season = changes.get(season)
			season.SeriesName = cloneString(seriesName)
			applyChild(&season)
			refreshSortName(&season)
			changes.put(season)
			episodes, err := s.children(ctx, season.ID, false)
			if err != nil {
				return err
			}
			for _, episode := range episodes {
				if episode.Kind != item.KindEpisode {
					continue
				}
				episode = changes.get(episode)
				episode.SeriesName = cloneString(seriesName)
				applyChild(&episode)
				changes.put(episode)
			}
		}
	case item.KindSeason:
		for _, episode := range children {
			if episode.Kind != item.KindEpisode {
				continue
			}
			episode = changes.get(episode)
			applyChild(&episode)
			changes.put(episode)
		}
	case item.KindMusicAlbum:
		for _, track := range children {
			track = changes.get(track)
			applyChild(&track)
			changes.put(track)
		}
	}
	return nil
}

func (s *Service) children(ctx context.Context, parentID dto.UUID, recursive bool) ([]item.Item, error) {
	result, err := s.items.Query(ctx, store.ItemQuery{ParentID: &parentID, Recursive: recursive})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (s *Service) publish(edited []item.Item) {
	if s.publisher == nil || len(edited) == 0 {
		return
	}
	target := edited[0]
	parentID := dto.NilUUID
	if target.ParentID != nil {
		parentID = *target.ParentID
	}
	collectionFolderID := dto.NilUUID
	if target.TopParentID != nil {
		collectionFolderID = *target.TopParentID
	}
	ids := make([]dto.UUID, len(edited))
	for index, value := range edited {
		ids[index] = value.ID
	}
	s.publisher.PublishItemUpdated(parentID, collectionFolderID, ids)
}

// changeSet keeps one edited copy per child so a child that two cascades
// touch persists once, in first-touch order.
type changeSet struct {
	order []dto.UUID
	items map[dto.UUID]item.Item
}

func newChangeSet() *changeSet { return &changeSet{items: map[dto.UUID]item.Item{}} }

func (c *changeSet) get(value item.Item) item.Item {
	if edited, found := c.items[value.ID]; found {
		return edited
	}
	return value
}

func (c *changeSet) put(value item.Item) {
	if _, found := c.items[value.ID]; !found {
		c.order = append(c.order, value.ID)
	}
	c.items[value.ID] = value
}

func (c *changeSet) ordered() []item.Item {
	result := make([]item.Item, 0, len(c.order))
	for _, id := range c.order {
		result = append(result, c.items[id])
	}
	return result
}

// MetadataEditorInfo returns the editor options for an item
// (ItemUpdateController.cs:142-186).
func (s *Service) MetadataEditorInfo(ctx context.Context, id dto.UUID) (dto.MetadataEditorInfo, error) {
	target, err := s.load(ctx, id)
	if err != nil {
		return dto.MetadataEditorInfo{}, err
	}
	info := dto.MetadataEditorInfo{
		ParentalRatingOptions: []dto.ParentalRating{},
		Countries:             []dto.CountryInfo{},
		Cultures:              []dto.CultureDto{},
		ExternalIdInfos:       []dto.ExternalIdInfo{},
		ContentTypeOptions:    []dto.NameValuePair{},
	}
	if s.localization != nil {
		country := ""
		if s.config != nil {
			country = s.config.System().MetadataCountryCode
		}
		info.ParentalRatingOptions = append(info.ParentalRatingOptions, s.localization.ParentalRatings(country)...)
		info.Countries = append(info.Countries, s.localization.Countries()...)
		info.Cultures = distinctCultures(s.localization.Cultures())
	}
	for _, descriptor := range providers.GetExternalIDInfos(target.Kind) {
		info.ExternalIdInfos = append(info.ExternalIdInfos, dto.ExternalIdInfo{Name: descriptor.ProviderName, Key: descriptor.Key, Type: descriptor.Type})
	}

	if !supportsContentType(target) {
		return info, nil
	}
	inherited, err := s.inheritedContentType(ctx, target)
	if err != nil {
		return dto.MetadataEditorInfo{}, err
	}
	configured := s.configuredContentType(containingFolderPath(target))
	if inherited != nil && configured == nil {
		return info, nil
	}
	info.ContentTypeOptions = contentTypeOptions()
	if configured != nil {
		value := configured.String()
		info.ContentType = &value
	}
	if inherited == nil || *inherited == item.CollectionTypeTVShows || *inherited == item.CollectionTypeMovies {
		filtered := info.ContentTypeOptions[:0]
		for _, option := range info.ContentTypeOptions {
			if strings.TrimSpace(option.Value) == "" || strings.EqualFold(option.Value, "TvShows") || strings.EqualFold(option.Value, "Movies") {
				filtered = append(filtered, option)
			}
		}
		info.ContentTypeOptions = filtered
	}
	return info, nil
}

// UpdateContentType records a content-type override for the item's
// containing folder in ServerConfiguration.ContentTypes
// (ItemUpdateController.cs:197-227).
func (s *Service) UpdateContentType(ctx context.Context, id dto.UUID, contentType string) error {
	target, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if s.config == nil {
		return errors.New("itemedit: configuration is unavailable")
	}
	path := containingFolderPath(target)
	return s.config.UpdateSystem(ctx, func(configuration *config.ServerConfiguration) error {
		types := make([]config.NameValuePair, 0, len(configuration.ContentTypes)+1)
		for _, pair := range configuration.ContentTypes {
			if strings.TrimSpace(pair.Name) == "" || strings.EqualFold(pair.Name, path) {
				continue
			}
			types = append(types, pair)
		}
		if strings.TrimSpace(contentType) != "" {
			types = append(types, config.NameValuePair{Name: path, Value: contentType})
		}
		configuration.ContentTypes = types
		return nil
	})
}

// supportsContentType mirrors the item filter at ItemUpdateController.cs:158-163.
func supportsContentType(target item.Item) bool {
	if target.IsVirtualItem || target.SourceType != item.SourceTypeLibrary {
		return false
	}
	switch target.Kind {
	case item.KindCollectionFolder, item.KindUserView, item.KindAggregateFolder, item.KindTvChannel, item.KindLiveTvChannel,
		item.KindPerson, item.KindGenre, item.KindMusicGenre, item.KindStudio, item.KindMusicArtist, item.KindYear:
		return false
	}
	return true
}

// inheritedContentType ports LibraryManager.GetInheritedContentType: the
// library's collection type, or else the outermost configured override on
// an ancestor folder.
func (s *Service) inheritedContentType(ctx context.Context, target item.Item) (*item.CollectionType, error) {
	if s.libraries != nil && target.TopParentID != nil {
		folders, err := s.libraries.VirtualFolders(ctx)
		if err != nil {
			return nil, err
		}
		for _, folder := range folders {
			if folder.CollectionFolderID != *target.TopParentID {
				continue
			}
			if value, ok := parseCollectionType(folder.CollectionType); ok {
				return &value, nil
			}
		}
	}
	var found *item.CollectionType
	current := target
	for depth := 0; current.ParentID != nil && !current.ParentID.IsNil() && depth < 64; depth++ {
		parents, err := s.items.GetBatch(ctx, []dto.UUID{*current.ParentID})
		if err != nil {
			return nil, fmt.Errorf("itemedit: load parent: %w", err)
		}
		if len(parents) == 0 {
			break
		}
		current = parents[0]
		if current.Kind == item.KindAggregateFolder {
			break
		}
		var value *item.CollectionType
		if current.Kind == item.KindCollectionFolder {
			if current.Extra.CollectionFolder != nil && current.Extra.CollectionFolder.CollectionType != nil {
				collection := *current.Extra.CollectionFolder.CollectionType
				value = &collection
			}
		} else {
			value = s.configuredContentType(containingFolderPath(current))
		}
		if value != nil {
			found = value
		}
	}
	return found, nil
}

// configuredContentType ports LibraryManager.GetContentTypeOverride without
// inheritance: the ContentTypes entry for exactly this path.
func (s *Service) configuredContentType(path string) *item.CollectionType {
	if s.config == nil || path == "" {
		return nil
	}
	for _, pair := range s.config.System().ContentTypes {
		if !samePath(pair.Name, path) {
			continue
		}
		// Enum.TryParse<CollectionType> is case-sensitive.
		for value := item.CollectionTypeUnknown; value <= item.CollectionTypeFolders; value++ {
			if value.String() == pair.Value {
				result := value
				return &result
			}
		}
		return nil
	}
	return nil
}

func (s *Service) collectionType(ctx context.Context, target item.Item) (item.CollectionType, error) {
	if s.libraries == nil || target.TopParentID == nil {
		return item.CollectionTypeUnknown, nil
	}
	folders, err := s.libraries.VirtualFolders(ctx)
	if err != nil {
		return item.CollectionTypeUnknown, err
	}
	for _, folder := range folders {
		if folder.CollectionFolderID == *target.TopParentID {
			value, _ := parseCollectionType(folder.CollectionType)
			return value, nil
		}
	}
	return item.CollectionTypeUnknown, nil
}

func parseCollectionType(value string) (item.CollectionType, bool) {
	for candidate := item.CollectionTypeMovies; candidate <= item.CollectionTypeFolders; candidate++ {
		if strings.EqualFold(candidate.String(), value) {
			return candidate, true
		}
	}
	return item.CollectionTypeUnknown, false
}

// containingFolderPath ports BaseItem.ContainingFolderPath.
func containingFolderPath(target item.Item) string {
	if target.Path == nil || *target.Path == "" {
		return ""
	}
	if target.IsFolder {
		return *target.Path
	}
	return filepath.Dir(*target.Path)
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// contentTypeOptions ports GetContentTypeOptions(true) with the stock en-US
// display strings (Localization/Core/en-US.json).
func contentTypeOptions() []dto.NameValuePair {
	return []dto.NameValuePair{
		{Name: "Inherit", Value: ""},
		{Name: "Movies", Value: "movies"},
		{Name: "Music", Value: "music"},
		{Name: "Shows", Value: "tvshows"},
		{Name: "Home Videos", Value: "homevideos"},
		{Name: "Music Videos", Value: "musicvideos"},
		{Name: "Photos", Value: "photos"},
	}
}

// distinctCultures mirrors the DistinctBy(DisplayName, OrdinalIgnoreCase)
// and OrderBy(DisplayName) at ItemUpdateController.cs:153-156, as the
// /Localization/Cultures handler does.
func distinctCultures(all []dto.CultureDto) []dto.CultureDto {
	unique := make([]dto.CultureDto, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, culture := range all {
		key := strings.ToLower(culture.DisplayName)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, culture)
	}
	sort.SliceStable(unique, func(i, j int) bool { return unique[i].DisplayName < unique[j].DisplayName })
	return unique
}
