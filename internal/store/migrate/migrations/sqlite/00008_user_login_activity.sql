-- +goose Up
ALTER TABLE users ADD COLUMN last_login_date INTEGER;
ALTER TABLE users ADD COLUMN last_activity_date INTEGER;

-- +goose Down
ALTER TABLE users DROP COLUMN last_activity_date;
ALTER TABLE users DROP COLUMN last_login_date;
