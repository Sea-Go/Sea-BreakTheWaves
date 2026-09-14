CREATE SCHEMA IF NOT EXISTS warehouse_search_source;

CREATE TABLE IF NOT EXISTS warehouse_search_source.consumer_cursor (
  consumer text PRIMARY KEY,
  producer text NOT NULL,
  committed_offset bigint NOT NULL DEFAULT 0 CHECK (committed_offset >= 0)
);

-- All offsets of the shared RTW Knowledge producer are retained. Non-qrel
-- events are explicit technical skips, never silently omitted from the cursor.
CREATE TABLE IF NOT EXISTS warehouse_search_source.ods_event (
  producer text NOT NULL,
  source_offset bigint NOT NULL CHECK (source_offset > 0),
  event_id text NOT NULL,
  event_type text NOT NULL,
  event_spec bytea NOT NULL,
  dc_input_hash text NOT NULL,
  dc_receipt jsonb NOT NULL,
  dc_received_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('qrel_revision', 'technical_skip')),
  authority_event_json bytea,
  authority_event_sha256 text,
  judgment_id text,
  judgment_revision_id text,
  judgment_revision integer CHECK (judgment_revision > 0),
  base_revision_id text,
  judgment_state text,
  judgment_payload jsonb,
  PRIMARY KEY (producer, source_offset),
  UNIQUE (producer, event_id),
  UNIQUE (producer, judgment_revision_id),
  CHECK ((status = 'technical_skip' AND authority_event_json IS NULL AND authority_event_sha256 IS NULL
          AND judgment_id IS NULL AND judgment_revision_id IS NULL AND judgment_revision IS NULL
          AND base_revision_id IS NULL
          AND judgment_state IS NULL AND judgment_payload IS NULL)
      OR (status = 'qrel_revision' AND authority_event_json IS NOT NULL AND authority_event_sha256 IS NOT NULL
          AND judgment_id IS NOT NULL AND judgment_revision_id IS NOT NULL AND judgment_revision IS NOT NULL
          AND base_revision_id IS NOT NULL
          AND judgment_state IS NOT NULL AND judgment_payload IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS warehouse_search_source_judgment_history
  ON warehouse_search_source.ods_event (judgment_id, source_offset DESC)
  WHERE status = 'qrel_revision';
