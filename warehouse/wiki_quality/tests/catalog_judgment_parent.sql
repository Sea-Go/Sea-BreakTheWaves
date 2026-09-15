-- Every typed Fact/Catalog and Judgment must be linked to its one parent
-- position and preserve the original RTW/DC hash domains.
select c.catalog_offset as bad_offset
from {{ ref('dwd_wiki_fact_catalog') }} c
left join {{ ref('dwd_wiki_source_prefix') }} p
  on p.producer=c.producer and p.source_offset=c.catalog_offset
where p.source_offset is null or p.status != 'fact_set_verified'
   or p.event_id != c.catalog_event_id
   or p.rtw_original_event_sha256 != c.catalog_event_raw_sha256
   or p.event_jcs_sha256 != c.catalog_event_jcs_sha256
   or p.dc_receipt_sha256 != c.catalog_dc_receipt_sha256
   or p.fact_set_payload_jcs_sha256 != c.fact_set_payload_jcs_sha256
union all
select j.source_offset as bad_offset
from {{ ref('dwd_wiki_fact_judgment') }} j
left join {{ ref('dwd_wiki_source_prefix') }} p
  on p.producer=j.producer and p.source_offset=j.source_offset
where p.source_offset is null or p.status != 'quality_verified'
   or p.event_id != j.event_id
   or p.rtw_original_event_sha256 != j.event_raw_sha256
   or p.event_jcs_sha256 != j.event_jcs_sha256
   or p.dc_receipt_sha256 != j.dc_receipt_sha256
