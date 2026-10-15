-- +goose Up
ALTER TABLE items ADD COLUMN media_source_container TEXT;

-- +goose Down
ALTER TABLE items DROP COLUMN media_source_container;
