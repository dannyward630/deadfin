-- +goose Up
-- Native control-plane schema. Six native aggregates, external
-- identity mappings, and the compatibility-item projection. The native graph
-- is a derived layer until a separate ratified decision changes this rule
-- (docs/08-control-plane.md:35-39).

CREATE TABLE native_works (
    id BLOB PRIMARY KEY,
    state TEXT NOT NULL DEFAULT 'Unidentified',
    title TEXT NOT NULL DEFAULT '',
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL
);

CREATE TABLE native_editions (
    id BLOB PRIMARY KEY,
    work_id BLOB NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL,
    FOREIGN KEY (work_id) REFERENCES native_works(id) ON DELETE CASCADE
);
CREATE INDEX ix_native_editions_work ON native_editions(work_id, id);

CREATE TABLE native_releases (
    id BLOB PRIMARY KEY,
    edition_id BLOB NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    acquisition_state TEXT NOT NULL DEFAULT 'Missing',
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL,
    FOREIGN KEY (edition_id) REFERENCES native_editions(id) ON DELETE CASCADE
);
CREATE INDEX ix_native_releases_edition ON native_releases(edition_id, id);

CREATE TABLE native_assets (
    id BLOB PRIMARY KEY,
    release_id BLOB NOT NULL,
    path TEXT NOT NULL DEFAULT '',
    device INTEGER NOT NULL DEFAULT 0,
    inode INTEGER NOT NULL DEFAULT 0,
    source_revision TEXT NOT NULL DEFAULT '',
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL,
    FOREIGN KEY (release_id) REFERENCES native_releases(id) ON DELETE CASCADE
);
CREATE INDEX ix_native_assets_release ON native_assets(release_id, id);
CREATE INDEX ix_native_assets_inode ON native_assets(device, inode);

CREATE TABLE native_inventories (
    id BLOB PRIMARY KEY,
    asset_id BLOB NOT NULL,
    state TEXT NOT NULL DEFAULT 'Absent',
    physical_bytes INTEGER NOT NULL DEFAULT 0,
    hardlinked INTEGER NOT NULL DEFAULT 0,
    hardlink_target TEXT NOT NULL DEFAULT '',
    seed_ratio REAL NOT NULL DEFAULT 0,
    seed_time INTEGER NOT NULL DEFAULT 0,
    seed_obligation_met INTEGER NOT NULL DEFAULT 0,
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL,
    FOREIGN KEY (asset_id) REFERENCES native_assets(id) ON DELETE CASCADE
);
CREATE INDEX ix_native_inventories_asset ON native_inventories(asset_id, id);

CREATE TABLE native_requests (
    id BLOB PRIMARY KEY,
    work_id BLOB,
    edition_id BLOB,
    state TEXT NOT NULL DEFAULT 'Submitted',
    approval_state TEXT NOT NULL DEFAULT 'Pending',
    fulfillment_id BLOB,
    date_created INTEGER NOT NULL,
    date_modified INTEGER NOT NULL,
    FOREIGN KEY (work_id) REFERENCES native_works(id) ON DELETE CASCADE,
    FOREIGN KEY (edition_id) REFERENCES native_editions(id) ON DELETE CASCADE,
    CHECK (
        (work_id IS NOT NULL AND edition_id IS NULL)
        OR (work_id IS NULL AND edition_id IS NOT NULL)
    )
);
CREATE INDEX ix_native_requests_work ON native_requests(work_id, id);
CREATE INDEX ix_native_requests_edition ON native_requests(edition_id, id);

-- External identity mappings for Works and Editions. Each row records the
-- catalog, the external ID, and the last-verified time
-- (docs/08-control-plane.md:76-80).
CREATE TABLE native_identity_mappings (
    aggregate_id BLOB NOT NULL,
    catalog TEXT NOT NULL,
    external_id TEXT NOT NULL,
    last_verified INTEGER NOT NULL,
    PRIMARY KEY (aggregate_id, catalog)
);
CREATE UNIQUE INDEX ix_native_identity_mappings_external ON native_identity_mappings(catalog, external_id);

-- Compatibility-item projection: maps a native UUID to one item.Item.ID
-- (docs/08-control-plane.md:46-50). The projection is idempotent: a repeated
-- write leaves one mapping. SetItemProjection checks the source revision
-- before it replaces a mapping, so a stale write returns
-- ErrStaleSourceRevision and leaves the mapping unchanged.
CREATE TABLE native_item_projections (
    native_id BLOB PRIMARY KEY,
    item_id BLOB NOT NULL
);

-- +goose Down
DROP TABLE native_item_projections;
DROP TABLE native_identity_mappings;
DROP TABLE native_requests;
DROP TABLE native_inventories;
DROP TABLE native_assets;
DROP TABLE native_releases;
DROP TABLE native_editions;
DROP TABLE native_works;
