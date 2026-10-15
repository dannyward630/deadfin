-- +goose Up
ALTER TABLE items ADD COLUMN file_device BIGINT;
ALTER TABLE items ADD COLUMN file_inode BIGINT;
ALTER TABLE items ADD COLUMN file_changed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE items DROP COLUMN file_changed_at;
ALTER TABLE items DROP COLUMN file_inode;
ALTER TABLE items DROP COLUMN file_device;
