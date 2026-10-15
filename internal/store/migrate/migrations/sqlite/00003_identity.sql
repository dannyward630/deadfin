-- +goose Up
-- User policy and configuration are opaque JSON blobs. Upstream's permissions and
-- preferences rows are flattened here because no query currently needs individual
-- facts; each fact therefore has one persisted owner.
CREATE TABLE users (
    id BLOB PRIMARY KEY,
    name TEXT NOT NULL,
    normalized_name TEXT NOT NULL UNIQUE,
    password_hash TEXT,
    policy BLOB NOT NULL,
    configuration BLOB NOT NULL
);

-- An access token is a device row, matching Jellyfin's security Device entity.
CREATE TABLE devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id BLOB NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access_token TEXT NOT NULL UNIQUE,
    app_name TEXT NOT NULL,
    app_version TEXT NOT NULL,
    device_name TEXT NOT NULL,
    device_id TEXT NOT NULL,
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL,
    date_last_activity INTEGER NOT NULL,
    UNIQUE (user_id, device_id)
);
CREATE INDEX ix_devices_user_device ON devices(user_id, device_id);

CREATE TABLE device_options (
    device_id TEXT PRIMARY KEY,
    custom_name TEXT
);

CREATE TABLE api_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    access_token TEXT NOT NULL UNIQUE,
    date_created INTEGER NOT NULL,
    date_last_activity INTEGER NOT NULL
);

CREATE TABLE display_preferences (
    user_id BLOB NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id BLOB NOT NULL,
    client TEXT NOT NULL,
    data BLOB NOT NULL,
    PRIMARY KEY (user_id, item_id, client)
);

CREATE TABLE custom_item_display_preferences (
    user_id BLOB NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id BLOB NOT NULL,
    client TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT,
    PRIMARY KEY (user_id, item_id, client, key)
);

CREATE TABLE access_schedules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id BLOB NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day_of_week INTEGER NOT NULL,
    start_hour REAL NOT NULL,
    end_hour REAL NOT NULL
);
CREATE INDEX ix_access_schedules_user ON access_schedules(user_id, id);

-- +goose Down
DROP TABLE access_schedules;
DROP TABLE custom_item_display_preferences;
DROP TABLE display_preferences;
DROP TABLE api_keys;
DROP TABLE device_options;
DROP TABLE devices;
DROP TABLE users;
