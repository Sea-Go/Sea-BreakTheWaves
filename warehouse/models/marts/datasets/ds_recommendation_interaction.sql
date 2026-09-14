select lower(hex(SHA256(toJSONString(tuple(authority_id, tenant_id, subject_id, request_id, impression_id, 'effective_read'))))) as sample_id,
       request_id, impression_id, authority_id, tenant_id, subject_id, item_id, content_revision,
       assumeNotNull(request_time) as request_time,
       impression_time, assumeNotNull(feature_cutoff) as feature_cutoff,
       assumeNotNull(feature_snapshot_ref) as feature_snapshot_ref,
       assumeNotNull(feature_contract_id) as feature_contract_id,
       assumeNotNull(user_interest) as user_interest,
       assumeNotNull(item_quality) as item_quality,
       assumeNotNull(feature_available_at) as feature_available_at,
       window_end as label_observation_end,
       assumeNotNull(label) as label, label_state, label_revision, toFloat64(1.0) as sampling_probability,
       multiIf(
         request_time >= parseDateTime64BestEffort({{ literal(var('splits')['train']['start']) }},6,'UTC')
         and request_time < parseDateTime64BestEffort({{ literal(var('splits')['train']['end']) }},6,'UTC'), 'train',
         request_time >= parseDateTime64BestEffort({{ literal(var('splits')['validation']['start']) }},6,'UTC')
         and request_time < parseDateTime64BestEffort({{ literal(var('splits')['validation']['end']) }},6,'UTC'), 'validation',
         request_time >= parseDateTime64BestEffort({{ literal(var('splits')['test']['start']) }},6,'UTC')
         and request_time < parseDateTime64BestEffort({{ literal(var('splits')['test']['end']) }},6,'UTC'), 'test',
         'out_of_range'
       ) as split
from {{ ref('dws_labels') }}
where qualification = 'eligible' and window_mature
  and label_state in ('POSITIVE', 'OBSERVED_NEGATIVE')
