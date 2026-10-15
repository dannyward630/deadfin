-- +goose Up
CREATE TABLE item_additional_parts (item_id UUID NOT NULL, path TEXT NOT NULL, sort_order INTEGER NOT NULL, PRIMARY KEY(item_id, sort_order));
CREATE UNIQUE INDEX ix_item_additional_parts_path ON item_additional_parts(item_id, path);

-- +goose Down
DROP INDEX ix_item_additional_parts_path;
DROP TABLE item_additional_parts;
