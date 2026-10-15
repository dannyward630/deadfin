-- +goose Up
ALTER TABLE users ADD COLUMN last_login_date TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN last_activity_date TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users DROP COLUMN last_activity_date;
ALTER TABLE users DROP COLUMN last_login_date;
