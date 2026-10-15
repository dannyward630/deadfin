-- +goose Up
-- A recursive ParentId query on a CollectionFolder or UserView selects items
-- whose top parent is the parent. This index lets that scope drive the query
-- instead of a scan of every item.
CREATE INDEX ix_items_top_parent ON items(top_parent_id);

-- +goose Down
DROP INDEX ix_items_top_parent;
