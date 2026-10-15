// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// SimilarItemsRequest is the provider input shared by the controller and the
// configured local or remote similarity adapters.
type SimilarItemsRequest struct {
	Item             item.Item
	ExcludeArtistIDs []dto.UUID
	UserID           *dto.UUID
	User             *store.User
	Limit            *int
	DtoOptions       store.ItemQueryDTOOptions
}

// SimilarItemsService selects and orders providers for one source item. Remote
// adapters may be supplied by the application when configured; the stock local
// service always remains available.
type SimilarItemsService interface {
	SimilarItems(context.Context, SimilarItemsRequest) ([]item.Item, error)
}

// NewLocalSimilarItemsService ports Jellyfin's stock local provider selection.
// It intentionally contains no remote provider: upstream only enables those
// when a library configuration explicitly names one.
func NewLocalSimilarItemsService(reader ItemReader) SimilarItemsService {
	return localSimilarItemsService{reader: reader}
}

type localSimilarItemsService struct {
	reader ItemReader
}

func (s localSimilarItemsService) SimilarItems(ctx context.Context, request SimilarItemsRequest) ([]item.Item, error) {
	if s.reader == nil {
		return nil, errNoSimilarItemsReader
	}
	limit := 50
	if request.Limit != nil {
		limit = *request.Limit
	}
	if limit == 0 {
		return []item.Item{}, nil
	}

	if request.Item.Kind == item.KindMovie || request.Item.Kind == item.KindTrailer {
		return s.movieSimilarItems(ctx, request, limit)
	}
	query, ok := localSimilarQuery(request, limit)
	if !ok {
		return []item.Item{}, nil
	}
	result, err := s.reader.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	return distinctSimilarItems(result.Items, request.Item, limit), nil
}

func localSimilarQuery(request SimilarItemsRequest, limit int) (store.ItemQuery, bool) {
	kind, ok := localSimilarKind(request.Item.Kind)
	if !ok {
		return store.ItemQuery{}, false
	}
	query := store.ItemQuery{
		Genres:                 append([]string(nil), request.Item.Values.Genres...),
		Tags:                   append([]string(nil), request.Item.Values.Tags...),
		ExcludeItemIDs:         []dto.UUID{request.Item.ID},
		IncludeItemTypes:       []item.Kind{kind},
		Limit:                  &limit,
		UserID:                 request.UserID,
		User:                   request.User,
		DtoOptions:             request.DtoOptions,
		EnableTotalRecordCount: true,
		SortBy:                 []store.ItemSortBy{store.ItemSortByRandom},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
	}
	// Series omits artist exclusions upstream; audio, albums, and artists pass
	// them through to their local genre/tag provider.
	if kind != item.KindSeries {
		query.ExcludeArtistIDs = append([]dto.UUID(nil), request.ExcludeArtistIDs...)
	}
	return query, true
}

// localSimilarItemsProviderName is the Name of the stock local
// similar-items providers ("Local Genre/Tag").
const localSimilarItemsProviderName = "Local Genre/Tag"

// localSimilarItemsSupported reports the kinds that the local service serves,
// as the stock ILocalSimilarItemsProvider set does.
func localSimilarItemsSupported(kind item.Kind) bool {
	if kind == item.KindMovie || kind == item.KindTrailer {
		return true
	}
	_, ok := localSimilarKind(kind)
	return ok
}

func localSimilarKind(kind item.Kind) (item.Kind, bool) {
	switch kind {
	case item.KindSeries, item.KindAudio, item.KindMusicAlbum, item.KindMusicArtist:
		return kind, true
	default:
		return 0, false
	}
}

// movieSimilarItems ports MovieSimilarItemsProvider's five weighted dimensions:
// genre (10), tag (5), studio (5), director (50), actor/guest star (15). The
// ItemReader seam supplies bounded candidate sets; final scoring and stable
// deduplication live here, where provider ordering belongs.
func (s localSimilarItemsService) movieSimilarItems(ctx context.Context, request SimilarItemsRequest, limit int) ([]item.Item, error) {
	base := store.ItemQuery{
		ExcludeItemIDs:         []dto.UUID{request.Item.ID},
		ExcludeArtistIDs:       append([]dto.UUID(nil), request.ExcludeArtistIDs...),
		IncludeItemTypes:       []item.Kind{item.KindMovie},
		IsMovie:                boolPtr(true),
		IsPlayed:               boolPtr(false),
		UserID:                 request.UserID,
		User:                   request.User,
		DtoOptions:             request.DtoOptions,
		EnableTotalRecordCount: false,
	}
	queries := movieCandidateQueries(base, request.Item)
	candidates := make([]item.Item, 0)
	seen := make(map[dto.UUID]struct{})
	for _, query := range queries {
		result, err := s.reader.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, candidate := range result.Items {
			if _, exists := seen[candidate.ID]; !exists {
				seen[candidate.ID] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
	}
	return rankMovieSimilarItems(candidates, request.Item, limit), nil
}

func movieCandidateQueries(base store.ItemQuery, source item.Item) []store.ItemQuery {
	queries := make([]store.ItemQuery, 0, 4)
	if len(source.Values.Genres) != 0 {
		query := base
		query.Genres = append([]string(nil), source.Values.Genres...)
		queries = append(queries, query)
	}
	if len(source.Values.Tags) != 0 {
		query := base
		query.Tags = append([]string(nil), source.Values.Tags...)
		queries = append(queries, query)
	}
	if len(source.Values.Studios) != 0 {
		query := base
		query.Studios = append([]string(nil), source.Values.Studios...)
		queries = append(queries, query)
	}
	if ids := scoredPersonIDs(source.People); len(ids) != 0 {
		query := base
		query.PersonIDs = ids
		queries = append(queries, query)
	}
	return queries
}

func rankMovieSimilarItems(candidates []item.Item, source item.Item, limit int) []item.Item {
	scores := make([]movieSimilarityScore, 0, len(candidates))
	for _, candidate := range candidates {
		if score := movieSimilarity(source, candidate); score != 0 {
			scores = append(scores, movieSimilarityScore{item: candidate, score: score})
		}
	}
	sort.SliceStable(scores, func(left, right int) bool {
		if scores[left].score != scores[right].score {
			return scores[left].score > scores[right].score
		}
		return scores[left].item.ID.Dashed() < scores[right].item.ID.Dashed()
	})
	items := make([]item.Item, 0, min(limit, len(scores)))
	keys := map[string]struct{}{similarPresentationKey(source): {}}
	for _, scored := range scores {
		key := similarPresentationKey(scored.item)
		if _, exists := keys[key]; exists {
			continue
		}
		keys[key] = struct{}{}
		items = append(items, scored.item)
		if len(items) == limit {
			break
		}
	}
	return items
}

type movieSimilarityScore struct {
	item  item.Item
	score int
}

func movieSimilarity(source, candidate item.Item) int {
	score := 10 * intersectionCount(source.Values.Genres, candidate.Values.Genres)
	score += 5 * intersectionCount(source.Values.Tags, candidate.Values.Tags)
	score += 5 * intersectionCount(source.Values.Studios, candidate.Values.Studios)
	sourcePeople := scoredPeople(source.People)
	for _, person := range candidate.People {
		if weight, ok := sourcePeople[person.PersonID]; ok {
			score += weight
		}
	}
	return score
}

func scoredPeople(people []item.PersonCredit) map[dto.UUID]int {
	result := make(map[dto.UUID]int, len(people))
	for _, person := range people {
		if weight := personScoreWeight(person.Type); weight > result[person.PersonID] {
			result[person.PersonID] = weight
		}
	}
	return result
}

func scoredPersonIDs(people []item.PersonCredit) []dto.UUID {
	weighted := scoredPeople(people)
	ids := make([]dto.UUID, 0, len(weighted))
	for id := range weighted {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left].Dashed() < ids[right].Dashed() })
	return ids
}

func personScoreWeight(kind item.PersonKind) int {
	switch kind {
	case item.PersonKindDirector:
		return 50
	case item.PersonKindActor, item.PersonKindGuestStar:
		return 15
	default:
		return 0
	}
}

func intersectionCount(left, right []string) int {
	present := make(map[string]struct{}, len(left))
	for _, value := range left {
		if clean := item.CleanName(strings.TrimSpace(value)); clean != "" {
			present[clean] = struct{}{}
		}
	}
	count := 0
	matched := make(map[string]struct{}, len(right))
	for _, value := range right {
		clean := item.CleanName(strings.TrimSpace(value))
		if _, duplicate := matched[clean]; clean == "" || duplicate {
			continue
		}
		if _, ok := present[clean]; ok {
			matched[clean] = struct{}{}
			count++
		}
	}
	return count
}

func distinctSimilarItems(values []item.Item, source item.Item, limit int) []item.Item {
	items := make([]item.Item, 0, min(limit, len(values)))
	keys := map[string]struct{}{similarPresentationKey(source): {}}
	for _, value := range values {
		key := similarPresentationKey(value)
		if _, exists := keys[key]; exists {
			continue
		}
		keys[key] = struct{}{}
		items = append(items, value)
		if len(items) == limit {
			break
		}
	}
	return items
}

func similarPresentationKey(value item.Item) string {
	if value.PresentationUniqueKey != nil && *value.PresentationUniqueKey != "" {
		return strings.ToLower(*value.PresentationUniqueKey)
	}
	return value.ID.Dashed()
}

func boolPtr(value bool) *bool { return &value }

var errNoSimilarItemsReader = similarItemsError("similar item reader is required")

type similarItemsError string

func (e similarItemsError) Error() string { return string(e) }
