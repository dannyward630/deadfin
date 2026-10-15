-- +goose Up
CREATE INDEX ix_user_data_user_item ON user_data(user_id, item_id);

-- +goose Down
DROP INDEX ix_user_data_user_item;
