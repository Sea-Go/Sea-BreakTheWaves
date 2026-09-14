CREATE SCHEMA IF NOT EXISTS warehouse_favorite;

CREATE TABLE IF NOT EXISTS warehouse_favorite.consumer_cursor (
  consumer text PRIMARY KEY,
  producer text NOT NULL,
  committed_offset bigint NOT NULL DEFAULT 0 CHECK (committed_offset >= 0)
);

CREATE TABLE IF NOT EXISTS warehouse_favorite.ods_event (
  producer text NOT NULL,
  source_offset bigint NOT NULL CHECK (source_offset > 0),
  event_id text NOT NULL,
  event_type text NOT NULL,
  aggregate_id text NOT NULL,
  aggregate_version bigint NOT NULL,
  event_spec jsonb NOT NULL,
  source_event_hash char(64) NOT NULL,
  technical_receipt jsonb NOT NULL,
  authority_id text NOT NULL,
  tenant_id text NOT NULL,
  subject_id text NOT NULL,
  favorite_id text NOT NULL,
  folder_id text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  target_revision text,
  operation text NOT NULL CHECK (operation IN ('assert','retract')),
  predecessor_event_id text,
  event_time timestamptz NOT NULL,
  available_at timestamptz NOT NULL,
  dc_received_at timestamptz NOT NULL,
  PRIMARY KEY (producer, source_offset),
  UNIQUE (producer, event_id)
);

CREATE INDEX IF NOT EXISTS ods_event_favorite_version
  ON warehouse_favorite.ods_event (producer, favorite_id, aggregate_version);
