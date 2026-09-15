-- One Catalog Fact joined with its last TRANSPORTED revision at the pinned
-- ACK cutoff. The receipt still cannot claim RTW as-of head or human truth.
with transported as (
  select *, row_number() over (
    partition by producer, fact_set_revision_id, fact_id
    order by source_offset desc
  ) as transported_rank
  from {{ ref('dwd_wiki_fact_judgment') }}
)
select
  c.producer, c.module_id, c.page_id, c.wiki_revision_id,
  c.fact_set_revision_id, c.source_scope_revision,
  c.catalog_offset, c.catalog_event_id,
  c.catalog_event_raw_sha256, c.catalog_event_jcs_sha256,
  c.catalog_dc_receipt_sha256, c.fact_set_payload_jcs_sha256,
  c.fact_id, c.required, c.conflict_group,
  c.source_revision_id, c.source_content_sha256,
  c.locator, c.source_byte_start, c.source_byte_end,
  c.source_quote, c.source_quote_sha256,
  c.facts_complete_declared, c.declaration_source, c.rtw_actor_id as catalog_actor_id,
  j.source_offset as transported_offset, j.event_id as transported_event_id,
  j.event_raw_sha256 as transported_raw_sha256,
  j.event_jcs_sha256 as transported_jcs_sha256,
  j.dc_receipt_sha256 as transported_dc_receipt_sha256,
  j.judgment_id, j.judge_revision_id, j.judge_revision,
  j.assessment as admin_assessment, j.claimed_grade as admin_claimed_grade,
  j.rtw_actor_id as judgment_actor_id,
  c.acknowledged_cutoff_offset,
  'rtw_dc_source_proof_only' as evidence_level,
  'not_evaluable' as quality_state,
  'none' as activation
from {{ ref('dwd_wiki_fact_catalog') }} c
left join transported j
  on j.producer = c.producer
  and j.wiki_revision_id = c.wiki_revision_id
  and j.fact_set_revision_id = c.fact_set_revision_id
  and j.source_scope_revision = c.source_scope_revision
  and j.fact_id = c.fact_id
  and j.source_revision_id = c.source_revision_id
  and j.source_quote_sha256 = c.source_quote_sha256
  and j.transported_rank = 1
