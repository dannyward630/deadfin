-- +goose Up
CREATE INDEX ix_user_data_user_item ON user_data(user_id, item_id);
-- Without statistics for the new index the planner takes user_id = ? on it
-- as selective and drives user-data joins from all of the user's rows.
ANALYZE user_data;

-- +goose Down
DROP INDEX ix_user_data_user_item;
