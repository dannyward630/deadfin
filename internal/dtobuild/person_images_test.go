// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderProjectsPersonPrimaryImageTags covers DtoService.AttachPeople:
// each credit carries its Person entity's Primary image tag and BlurHash, so
// clients request cast photos. A person without a Primary image has no tag.
func TestBuilderProjectsPersonPrimaryImageTags(t *testing.T) {
	items := openBuilderItemStore(t)
	alice, bob := library.PersonEntity("Alice"), library.PersonEntity("Bob")
	hash := "LEHV6nWB2yk8"
	alice.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/alice.jpg", BlurHash: &hash}}
	name, first, second := "Movie", 0, 1
	movie := item.Item{ID: builderUUID(t, 21), Kind: item.KindMovie, Name: &name, People: []item.PersonCredit{
		{PersonID: alice.ID, Name: "Alice", Type: item.PersonKindActor, SortOrder: &first},
		{PersonID: bob.ID, Name: "Bob", Type: item.PersonKindDirector, SortOrder: &second},
	}}
	for _, value := range []item.Item{alice, bob, movie} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	loaded, err := items.Get(t.Context(), movie.ID)
	if err != nil {
		t.Fatalf("load movie: %v", err)
	}
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil))
	result, err := builder.Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{Fields: []string{"People"}}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wire := builderWireObject(t, result.Items[0])
	got := string(wire["People"])
	wantPeople := `[{"Id":"` + alice.ID.String() + `","Name":"Alice","Role":"","Type":"Actor","PrimaryImageTag":"alice.jpg","ImageBlurHashes":{"Primary":{"alice.jpg":"LEHV6nWB2yk8"}}},{"Id":"` + bob.ID.String() + `","Name":"Bob","Role":"","Type":"Director"}]`
	if got != wantPeople {
		t.Fatalf("People = %s, want %s", got, wantPeople)
	}
}
