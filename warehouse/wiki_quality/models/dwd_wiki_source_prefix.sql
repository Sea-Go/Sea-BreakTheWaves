-- Full acknowledged producer positions, including unrelated technical
-- skips. This is a transport witness, not a source of Wiki quality labels.
select
  producer, source_offset, event_id, event_type, status,
  event_spec_jcs, event_jcs_sha256,
  dc_receipt, dc_receipt_sha256,
  rtw_original_event, rtw_original_event_sha256,
  fact_set_payload_jcs, fact_set_payload_jcs_sha256,
  acknowledged_cutoff_offset, ods_evidence_sha256, dc_index_sha256
from {{ source('wiki_quality_landing', 'ods_wiki_prefix_event') }}
where producer = 'ridethewind.knowledge'
  and acknowledged_cutoff_offset = {{ var('cutoff_offset') }}
  and source_offset <= {{ var('cutoff_offset') }}
