-- +goose Up
-- Native control-plane schema. Six native aggregates, external
-- identity mappings, and the compatibility-item projection. The native graph
-- is a derived layer until a separate ratified decision changes this rule
-- (docs/08-control-plane.md:35-39).

CREATE TABLE native_works (
    id UUID PRIMARY KEY,
    state TEXT NOT NULL DEFAULT 'Unidentified',
    title TEXT NOT NULL DEFAULT '',
    date_created TIMESTAMPTZ NOT NULL,
    date_modified TIMESTAMPTZ NOT NULL
);

CREATE TABLE native_editions (
    id UUID PRIMARY KEY,
    work_id UUID NOT NULL REFERENCES native_works(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    date_created TIMESTAMPTZ NOT NULL,
    date_modified TIMESTAMPTZ NOT NULL
);
CREATE INDEX ix_native_editions_work ON native_editions(work_id, id);

CREATE TABLE native_releases (
    id UUID PRIMARY KEY,
    edition_id UUID NOT NULL REFERENCES native_editions(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    acquisition_state TEXT NOT NULL DEFAULT 'Missing',
    date_created TIMESTAMPTZ NOT NULL,
    date_modified TIMESTAMPTZ NOT NULL
);
CREATE INDEX ix_native_releases_edition ON native_releases(edition_id, id);

CREATE TABLE native_assets (
    id UUID PRIMARY KEY,
    release_id UUID NOT NULL REFERENCES native_releases(id) ON DELETE CASCADE,
    path TEXT NOT NULL DEFAULT '',
    device BIGINT NOT NULL DEFAULT 0,
    inode BIGINT NOT NULL DEFAULT 0,
    source_revision TEXT NOT NULL DEFAULT '',
    date_created TIMESTAMPTZ NOT NULL,
    date_modified TIMESTAMPTZ NOT NULL
);
CREATE INDEX ix_native_assets_release ON native_assets(release_id, id);
CREATE INDEX ix_native_assets_inode ON native_assets(device, inode);

CREATE TABLE native_inventories (
    id UUID PRIMARY KEY,
    asset_id UUID NOT NULL REFERENCES native_assets(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'Absent',
    physical_bytes BIGINT NOT NULL DEFAULT 0,
    hardlinked BOOLEAN NOT NULL DEFAULT FALSE,
    hardlink_target TEXT NOT NULL DEFAULT '',
    seed_ratio DOUBLE PRECISION NOT NULL DEFAULT 0,
    seed_time BIGINT NOT NULL DEFAULT 0,
    seed_obligation_met BOOLEAN NOT NULL DEFAULT FALSE,
    date_created TIMESTAMPTZ NOT NULL,
    date_modified TIMESTAMPTZ NOT NULL
);

CREATE TABLE native_requests (
    id UUID PRIMARY KEY,
    work_id UUID REFERENCES native_works(id) ON DELETE CASCADE,
    edition_id UUID REFERENCES native_editions(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'Submitted',
    approval_state TEXT NOT NULL DEFAULT 'Pending',
    fulfillment_id UUID,
    date_created TIMESTAMPTZ NOT NULL,
    date_modified TIMESTAMPTZ NOT NULL,
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
    aggregate_id UUID NOT NULL,
    catalog TEXT NOT NULL,
    external_id TEXT NOT NULL,
    last_verified TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (aggregate_id, catalog)
);
CREATE UNIQUE INDEX ix_native_identity_mappings_external ON native_identity_mappings(catalog, external_id);

-- Compatibility-item projection: maps a native UUID to one item.Item.ID
-- (docs/08-control-plane.md:46-50). The projection is idempotent: a repeated
-- write leaves one mapping. SetItemProjection checks the source revision
-- before it replaces a mapping, so a stale write returns
-- ErrStaleSourceRevision and leaves the mapping unchanged.
CREATE TABLE native_item_projections (
    native_id UUID PRIMARY KEY,
    item_id UUID NOT NULL
);
CREATE INDEX ix_native_item_projections_item ON native_item_projections(item_id);

-- +goose Down
DROP TABLE native_item_projections;
DROP TABLE native_identity_mappings;
DROP TABLE native_requests;
DROP TABLE native_inventories;
DROP TABLE native_assets;
DROP TABLE native_releases;
DROP TABLE native_editions;
DROP TABLE native_works;
