-- +goose Up
DELETE FROM item_additional_parts AS additional_parts
WHERE NOT EXISTS (SELECT 1 FROM items WHERE items.id = additional_parts.item_id);
ALTER TABLE item_additional_parts
    ADD CONSTRAINT fk_item_additional_parts_item
    FOREIGN KEY(item_id) REFERENCES items(id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE item_additional_parts
    DROP CONSTRAINT fk_item_additional_parts_item;
