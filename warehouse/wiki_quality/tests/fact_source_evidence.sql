-- ADS is a provenance view at cutoff, never D07. Each required Fact needs
-- a transported judgment and the selected offset must be its latest one.
with last_transport as (
  select producer, fact_set_revision_id, fact_id,
         max(source_offset) as last_offset
  from {{ ref('dwd_wiki_fact_judgment') }}
  group by producer, fact_set_revision_id, fact_id
)
select c.fact_id
from {{ ref('ads_wiki_fact_source_evidence') }} c
left join last_transport l
  on l.producer=c.producer
  and l.fact_set_revision_id=c.fact_set_revision_id
  and l.fact_id=c.fact_id
where c.quality_state != 'not_evaluable'
   or c.evidence_level != 'rtw_dc_source_proof_only'
   or c.activation != 'none'
   or c.facts_complete_declared != true
   or (c.required and c.transported_offset is null)
   or (c.transported_offset is not null and
       c.transported_offset != l.last_offset)
   or c.acknowledged_cutoff_offset != {{ var('cutoff_offset') }}
union all
select c.fact_id
from {{ ref('dwd_wiki_fact_catalog') }} c
left join {{ ref('ads_wiki_fact_source_evidence') }} a
  on a.fact_set_revision_id=c.fact_set_revision_id and a.fact_id=c.fact_id
where a.fact_id is null
