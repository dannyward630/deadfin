-- name: StoreMetadata :one
SELECT value
FROM store_metadata
WHERE key = ?;
