-- +goose Up
ALTER TABLE items ADD COLUMN file_device INTEGER;
ALTER TABLE items ADD COLUMN file_inode INTEGER;
ALTER TABLE items ADD COLUMN file_changed_at INTEGER;

-- +goose Down
ALTER TABLE items DROP COLUMN file_changed_at;
ALTER TABLE items DROP COLUMN file_inode;
ALTER TABLE items DROP COLUMN file_device;
