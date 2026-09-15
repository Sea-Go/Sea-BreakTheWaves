CREATE SCHEMA IF NOT EXISTS warehouse_wiki_quality;

CREATE TABLE IF NOT EXISTS warehouse_wiki_quality.consumer_cursor (
  consumer text PRIMARY KEY,
  producer text NOT NULL,
  committed_offset bigint NOT NULL DEFAULT 0 CHECK (committed_offset >= 0)
);

-- The shared RTW producer is continuous. A quality judgment is only recorded
-- after verifying RTW's immutable original bytes and its frozen rubric.
CREATE TABLE IF NOT EXISTS warehouse_wiki_quality.ods_event (
  producer text NOT NULL,
  source_offset bigint NOT NULL CHECK (source_offset > 0),
  event_id text NOT NULL,
  event_type text NOT NULL,
  event_spec bytea NOT NULL,
  dc_input_hash text NOT NULL,
  dc_receipt bytea NOT NULL,
  dc_received_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('quality_verified', 'technical_skip')),
  authority_event_json bytea,
  authority_event_sha256 text,
  PRIMARY KEY (producer, source_offset),
  UNIQUE (producer, event_id),
  CHECK ((status = 'technical_skip' AND authority_event_json IS NULL AND authority_event_sha256 IS NULL)
      OR (status = 'quality_verified' AND authority_event_json IS NOT NULL AND authority_event_sha256 IS NOT NULL))
);
