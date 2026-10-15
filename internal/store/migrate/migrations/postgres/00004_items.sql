-- +goose Up
CREATE TABLE items (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL,
    parent_id UUID,
    top_parent_id UUID,
    series_id UUID,
    season_id UUID,
    channel_id UUID,
    primary_version_id UUID,
    owner_id UUID,
    playlist_owner_user_id UUID NOT NULL,
    path TEXT,
    name TEXT,
    sort_name TEXT,
    clean_name TEXT,
    forced_sort_name TEXT,
    original_title TEXT,
    original_language TEXT,
    overview TEXT,
    tagline TEXT,
    official_rating TEXT,
    custom_rating TEXT,
    presentation_unique_key TEXT,
    series_presentation_unique_key TEXT,
    album TEXT,
    external_id TEXT,
    external_series_id TEXT,
    external_service_id TEXT,
    preferred_metadata_language TEXT,
    preferred_metadata_country_code TEXT,
    series_name TEXT,
    season_name TEXT,
    episode_title TEXT,
    show_id TEXT,
    container TEXT,
    start_date TIMESTAMPTZ,
    end_date TIMESTAMPTZ,
    premiere_date TIMESTAMPTZ,
    date_created TIMESTAMPTZ,
    date_modified TIMESTAMPTZ,
    date_last_refreshed TIMESTAMPTZ,
    date_last_saved TIMESTAMPTZ,
    date_last_media_added TIMESTAMPTZ,
    production_year INTEGER,
    index_number INTEGER,
    parent_index_number INTEGER,
    width INTEGER,
    height INTEGER,
    total_bitrate INTEGER,
    inherited_parental_rating_value INTEGER,
    inherited_parental_rating_subvalue INTEGER,
    airs_before_season_number INTEGER,
    airs_after_season_number INTEGER,
    airs_before_episode_number INTEGER,
    run_time_ticks BIGINT,
    size BIGINT,
    community_rating REAL,
    critic_rating REAL,
    lufs REAL,
    normalization_gain REAL,
    unrated_type TEXT,
    source_type TEXT NOT NULL,
    media_type TEXT,
    extra_type TEXT,
    video_type TEXT,
    iso_type TEXT,
    video_3d_format TEXT,
    series_status TEXT,
    program_audio TEXT,
    is_folder BOOLEAN NOT NULL,
    is_virtual_item BOOLEAN NOT NULL,
    is_in_mixed_folder BOOLEAN NOT NULL,
    is_locked BOOLEAN NOT NULL,
    is_movie BOOLEAN NOT NULL,
    is_series BOOLEAN NOT NULL,
    is_repeat BOOLEAN NOT NULL,
    is_placeholder BOOLEAN NOT NULL,
    open_access BOOLEAN NOT NULL,
    extra JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX ix_items_parent ON items(parent_id);
CREATE INDEX ix_items_kind_top ON items(kind, top_parent_id);
CREATE INDEX ix_items_clean_name ON items(clean_name);
CREATE INDEX ix_items_presentation_unique_key ON items(presentation_unique_key);
CREATE INDEX ix_items_path ON items(path);

CREATE TABLE item_images (item_id UUID NOT NULL, type TEXT NOT NULL, idx INTEGER NOT NULL, path TEXT NOT NULL, date_modified TIMESTAMPTZ, width INTEGER NOT NULL, height INTEGER NOT NULL, blurhash TEXT, PRIMARY KEY(item_id, type, idx));
CREATE TABLE provider_ids (item_id UUID NOT NULL, provider TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(item_id, provider));
CREATE TABLE locked_fields (item_id UUID NOT NULL, field TEXT NOT NULL, PRIMARY KEY(item_id, field));
CREATE TABLE trailer_types (item_id UUID NOT NULL, type TEXT NOT NULL, PRIMARY KEY(item_id, type));
CREATE TABLE item_values (id BIGSERIAL PRIMARY KEY, kind TEXT NOT NULL, value TEXT NOT NULL, clean_value TEXT NOT NULL, UNIQUE(kind, value));
CREATE TABLE item_value_map (item_id UUID NOT NULL, value_id BIGINT NOT NULL REFERENCES item_values(id), sort_order INTEGER NOT NULL, PRIMARY KEY(item_id, value_id));
CREATE INDEX ix_item_value_map_item_order ON item_value_map(item_id, sort_order, value_id);
CREATE TABLE linked_children (parent_item_id UUID NOT NULL, child_item_id UUID NOT NULL, type TEXT NOT NULL, sort_order INTEGER, PRIMARY KEY(parent_item_id, child_item_id));
CREATE INDEX ix_linked_children_child ON linked_children(child_item_id);
CREATE INDEX ix_linked_children_parent_order ON linked_children(parent_item_id, sort_order, child_item_id);
CREATE TABLE ancestors (item_id UUID NOT NULL, ancestor_id UUID NOT NULL, PRIMARY KEY(ancestor_id, item_id));
CREATE INDEX ix_ancestors_item ON ancestors(item_id, ancestor_id);
CREATE TABLE user_data (user_id UUID NOT NULL, custom_key TEXT NOT NULL, item_id UUID NOT NULL, played BOOLEAN NOT NULL DEFAULT FALSE, position_ticks BIGINT NOT NULL DEFAULT 0, play_count INTEGER NOT NULL DEFAULT 0, is_favorite BOOLEAN NOT NULL DEFAULT FALSE, rating REAL, likes INTEGER, last_played TIMESTAMPTZ, audio_stream_index INTEGER, subtitle_stream_index INTEGER, PRIMARY KEY(user_id, custom_key));
CREATE TABLE item_shares (item_id UUID NOT NULL, user_id UUID NOT NULL, can_edit BOOLEAN NOT NULL, PRIMARY KEY(item_id, user_id));
CREATE TABLE boxset_library_folders (item_id UUID NOT NULL, library_folder_id UUID NOT NULL, PRIMARY KEY(item_id, library_folder_id));
CREATE TABLE people (id UUID PRIMARY KEY, name TEXT NOT NULL, clean_name TEXT NOT NULL);
CREATE TABLE people_map (item_id UUID NOT NULL, person_id UUID NOT NULL REFERENCES people(id), role TEXT, type TEXT NOT NULL, sort_order INTEGER, PRIMARY KEY(item_id, person_id, type));
CREATE INDEX ix_people_map_item_order ON people_map(item_id, sort_order, person_id);
CREATE TABLE media_streams (item_id UUID NOT NULL REFERENCES items(id) ON DELETE CASCADE, stream_index INTEGER NOT NULL, stream_type TEXT NOT NULL, codec TEXT, language TEXT, display_title TEXT, is_default BOOLEAN NOT NULL DEFAULT FALSE, is_forced BOOLEAN NOT NULL DEFAULT FALSE, is_external BOOLEAN NOT NULL DEFAULT FALSE, bitrate INTEGER, width INTEGER, height INTEGER, channels INTEGER, sample_rate INTEGER, extra JSONB NOT NULL DEFAULT '{}'::jsonb, PRIMARY KEY(item_id, stream_index));
CREATE TABLE media_attachments (item_id UUID NOT NULL REFERENCES items(id) ON DELETE CASCADE, attachment_index INTEGER NOT NULL, filename TEXT, mime_type TEXT, extra JSONB NOT NULL DEFAULT '{}'::jsonb, PRIMARY KEY(item_id, attachment_index));
CREATE TABLE chapters (item_id UUID NOT NULL REFERENCES items(id) ON DELETE CASCADE, chapter_index INTEGER NOT NULL, start_position_ticks BIGINT NOT NULL, name TEXT, image_path TEXT, extra JSONB NOT NULL DEFAULT '{}'::jsonb, PRIMARY KEY(item_id, chapter_index));
CREATE TABLE libraries (id UUID PRIMARY KEY, name TEXT NOT NULL, path TEXT NOT NULL, collection_type TEXT, options JSONB NOT NULL DEFAULT '{}'::jsonb);

-- +goose Down
DROP TABLE libraries;
DROP TABLE chapters;
DROP TABLE media_attachments;
DROP TABLE media_streams;
DROP TABLE people_map;
DROP TABLE people;
DROP TABLE boxset_library_folders;
DROP TABLE item_shares;
DROP TABLE user_data;
DROP TABLE ancestors;
DROP TABLE linked_children;
DROP TABLE item_value_map;
DROP TABLE item_values;
DROP TABLE trailer_types;
DROP TABLE locked_fields;
DROP TABLE provider_ids;
DROP TABLE item_images;
DROP TABLE items;
