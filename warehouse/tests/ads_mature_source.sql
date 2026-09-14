select authority_id, tenant_id, subject_id, impression_id
from {{ ref('ads_recommendation_exposures') }}
where evaluation_state in ('mature_positive', 'mature_negative')
  and (
    impression_id = '' or served_event_id is null or served_event_id = ''
    or impression_source_partition is null or impression_source_partition = ''
    or impression_source_sequence is null or impression_source_sequence = 0
    or served_source_partition is null or served_source_partition = ''
    or served_source_sequence is null or served_source_sequence = 0
    or impression_available_at > data_as_of or served_available_at > data_as_of
    or not window_mature or mature_label is null
    or not ((source_kind = 'synthetic' and source_coverage_state = 'synthetic_fixture_complete')
      or (source_kind = 'observed' and source_coverage_state = 'verified_complete'
        and match(coverage_receipt_sha256, '^[a-f0-9]{64}$')))
  )
