-- The warehouse may keep a technical position, but never turn it into a
-- Catalog/Judgment or discard the original-byte/receipt hash domains.
select source_offset
from {{ ref('dwd_wiki_source_prefix') }}
where lower(hex(SHA256(event_spec_jcs))) != event_jcs_sha256
   or lower(hex(SHA256(dc_receipt))) != dc_receipt_sha256
   or (
     status = 'technical_skip' and
       (rtw_original_event != '' or rtw_original_event_sha256 != ''
        or fact_set_payload_jcs != '' or fact_set_payload_jcs_sha256 != '')
   )
   or (
     status in ('quality_verified','fact_set_verified') and
       (rtw_original_event = '' or
        lower(hex(SHA256(rtw_original_event))) != rtw_original_event_sha256)
   )
   or status not in ('technical_skip','quality_verified','fact_set_verified')
