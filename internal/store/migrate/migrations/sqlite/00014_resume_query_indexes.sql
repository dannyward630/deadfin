-- +goose Up
CREATE INDEX ix_user_data_user_in_progress ON user_data(user_id, position_ticks, item_id, custom_key, last_played);
CREATE INDEX ix_items_primary_version_id ON items(primary_version_id, id);

-- +goose Down
DROP INDEX ix_items_primary_version_id;
DROP INDEX ix_user_data_user_in_progress;
