-- Preserve one row per declared Fact in the explicit historical Catalog.
-- The declaration does not establish objective completeness or page quality.
select
  producer, catalog_offset, catalog_event_id,
  catalog_event_raw_sha256, catalog_event_jcs_sha256,
  catalog_dc_receipt_sha256, fact_set_payload_jcs_sha256,
  module_id, page_id, wiki_revision_id, fact_set_revision_id,
  source_scope_revision, fact_id, source_revision_id,
  source_content_sha256, locator, source_byte_start, source_byte_end,
  source_quote, source_quote_sha256, required, conflict_group,
  facts_complete_declared, declaration_source, rtw_actor_id,
  acknowledged_cutoff_offset, quality_state
from {{ source('wiki_quality_landing', 'ods_wiki_catalog_fact') }}
where wiki_revision_id = '{{ var('wiki_revision_id') }}'
  and fact_set_revision_id = '{{ var('fact_set_revision_id') }}'
  and source_scope_revision = '{{ var('source_scope_revision') }}'
  and acknowledged_cutoff_offset = {{ var('cutoff_offset') }}
