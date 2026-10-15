-- +goose Up
CREATE TABLE item_user_data_keys (
    item_id UUID NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    custom_key TEXT NOT NULL,
    is_live_tv_channel_name BOOLEAN NOT NULL,
    PRIMARY KEY(item_id, custom_key)
);
CREATE INDEX ix_item_user_data_keys_lookup ON item_user_data_keys(item_id, is_live_tv_channel_name, custom_key);
CREATE INDEX ix_item_user_data_keys_key ON item_user_data_keys(custom_key, item_id);

-- +goose Down
DROP TABLE item_user_data_keys;
