-- +goose Up
CREATE TABLE keyframes (
    item_id UUID PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    total_duration_ticks BIGINT NOT NULL,
    keyframe_ticks JSONB
);
CREATE TABLE trickplay_infos (
    item_id UUID NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    tile_width INTEGER NOT NULL,
    tile_height INTEGER NOT NULL,
    thumbnail_count INTEGER NOT NULL,
    interval_ms INTEGER NOT NULL,
    bandwidth INTEGER NOT NULL,
    generation_token TEXT NOT NULL,
    PRIMARY KEY(item_id, width)
);

-- +goose Down
DROP TABLE trickplay_infos;
DROP TABLE keyframes;
