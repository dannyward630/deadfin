-- +goose Up
CREATE INDEX ix_provider_ids_provider_value_item ON provider_ids(provider, value, item_id);
CREATE INDEX ix_item_value_map_value_item ON item_value_map(value_id, item_id);
CREATE INDEX ix_people_map_person_type_item ON people_map(person_id, type, item_id);
CREATE INDEX ix_media_streams_type_codec_item ON media_streams(stream_type, codec, item_id);
CREATE INDEX ix_items_series ON items(series_id);
CREATE INDEX ix_item_shares_user_item ON item_shares(user_id, item_id);

-- +goose Down
DROP INDEX ix_item_shares_user_item;
DROP INDEX ix_items_series;
DROP INDEX ix_media_streams_type_codec_item;
DROP INDEX ix_people_map_person_type_item;
DROP INDEX ix_item_value_map_value_item;
DROP INDEX ix_provider_ids_provider_value_item;
