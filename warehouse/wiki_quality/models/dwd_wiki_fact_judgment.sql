-- Each row is a single transported Admin revision. No grade is promoted to
-- an independent D07 label by this provenance-only DWD.
select
  producer, source_offset, event_id, event_raw_sha256,
  event_jcs_sha256, dc_receipt_sha256,
  module_id, page_id, wiki_revision_id, fact_set_revision_id,
  source_scope_revision, fact_id, judgment_id, judge_revision_id,
  judge_revision, base_judge_revision_id, source_revision_id,
  source_quote_sha256, assessment, claimed_grade,
  claimed_citation_present, rtw_actor_id,
  acknowledged_cutoff_offset, quality_state
from {{ source('wiki_quality_landing', 'ods_wiki_judgment_revision') }}
where wiki_revision_id = '{{ var('wiki_revision_id') }}'
  and fact_set_revision_id = '{{ var('fact_set_revision_id') }}'
  and source_scope_revision = '{{ var('source_scope_revision') }}'
  and acknowledged_cutoff_offset = {{ var('cutoff_offset') }}
  and source_offset <= {{ var('cutoff_offset') }}
