-- +goose Up
CREATE TABLE item_additional_parts_new (
    item_id BLOB NOT NULL,
    path TEXT NOT NULL,
    sort_order INTEGER NOT NULL,
    PRIMARY KEY(item_id, sort_order),
    FOREIGN KEY(item_id) REFERENCES items(id) ON DELETE CASCADE
);
INSERT INTO item_additional_parts_new(item_id, path, sort_order)
SELECT additional_parts.item_id, additional_parts.path, additional_parts.sort_order
FROM item_additional_parts AS additional_parts
JOIN items ON items.id = additional_parts.item_id;
DROP TABLE item_additional_parts;
ALTER TABLE item_additional_parts_new RENAME TO item_additional_parts;
CREATE UNIQUE INDEX ix_item_additional_parts_path ON item_additional_parts(item_id, path);

-- +goose Down
CREATE TABLE item_additional_parts_without_cascade (
    item_id BLOB NOT NULL,
    path TEXT NOT NULL,
    sort_order INTEGER NOT NULL,
    PRIMARY KEY(item_id, sort_order)
);
INSERT INTO item_additional_parts_without_cascade(item_id, path, sort_order)
SELECT item_id, path, sort_order FROM item_additional_parts;
DROP TABLE item_additional_parts;
ALTER TABLE item_additional_parts_without_cascade RENAME TO item_additional_parts;
CREATE UNIQUE INDEX ix_item_additional_parts_path ON item_additional_parts(item_id, path);
