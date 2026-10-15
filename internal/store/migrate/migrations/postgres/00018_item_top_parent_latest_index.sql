-- +goose Up
-- Latest for a movies library walks the library's newest items first and
-- stops at its limit. This index serves that walk, in the order of the
-- grouped statement, without a sort of the whole library.
CREATE INDEX ix_items_top_parent_latest ON items(top_parent_id, is_folder, date_created DESC NULLS LAST, presentation_unique_key, id DESC);

-- +goose Down
DROP INDEX ix_items_top_parent_latest;
