-- One row per client-visible impression. DWD already removes non-visible
-- server emits, but a visible event alone is not a served or mature sample.
with evidence as (
  select d.authority_id as authority_id, d.tenant_id as tenant_id,
         d.subject_id as subject_id, d.request_id as request_id,
         d.impression_id as impression_id, d.item_id as item_id,
         d.content_revision as content_revision, d.impression_time as impression_time,
         d.window_end as window_end, d.qualification as qualification,
         d.label_state as dws_label_state,
         d.window_mature as window_mature, d.label_revision as label_revision,
         i.event_id as impression_event_id, i.available_at as impression_available_at,
         ie.source_partition as impression_source_partition,
         ie.source_sequence as impression_source_sequence,
         c.event_id as served_event_id, c.available_at as served_available_at,
         ce.source_partition as served_source_partition,
         ce.source_sequence as served_source_sequence,
         p.sourced_positive_count as sourced_positive_count,
         p.first_positive_available_at as first_positive_available_at
  from {{ ref('dws_labels') }} d
  left join {{ ref('ads_positive_evidence') }} p on {{ subject_equals('d', 'p') }}
    and d.impression_id = p.impression_id
  join {{ ref('dwd_impressions') }} i on {{ subject_equals('d', 'i') }}
    and d.impression_id = i.impression_id
  left join {{ ref('dwd_events') }} ie on {{ subject_equals('i', 'ie') }}
    and i.event_id = ie.event_id and ie.event_type = 'impression'
  left join {{ ref('dwd_candidates') }} c on {{ subject_equals('i', 'c') }}
    and i.request_id = c.request_id and i.candidate_id = c.candidate_id
    and i.item_id = c.item_id and i.content_revision = c.content_revision
    and i.stage_invocation_id = c.stage_invocation_id and i.attempt = c.attempt
    and c.stage = 'served'
  left join {{ ref('dwd_events') }} ce on {{ subject_equals('c', 'ce') }}
    and c.event_id = ce.event_id and ce.event_type = 'candidate'
),
classified as (
  select *, multiIf(
    impression_id = '' or impression_event_id = '', 'missing_impression_id',
    served_event_id is null, 'not_served',
    qualification != 'eligible', concat('excluded:', qualification),
    impression_source_partition is null or impression_source_partition = ''
      or impression_source_sequence is null or impression_source_sequence = 0
      or served_source_partition is null or served_source_partition = ''
      or served_source_sequence is null or served_source_sequence = 0
      or impression_available_at > parseDateTime64BestEffort({{ literal(var('ads_evaluation_cutoff', var('event_watermark'))) }}, 6, 'UTC')
      or served_available_at > parseDateTime64BestEffort({{ literal(var('ads_evaluation_cutoff', var('event_watermark'))) }}, 6, 'UTC'),
      'source_unavailable',
    not (
      ({{ literal(var('ads_source_kind', 'unverified')) }} = 'synthetic'
        and {{ literal(var('ads_source_coverage_state', 'unverified')) }} = 'synthetic_fixture_complete')
      or ({{ literal(var('ads_source_kind', 'unverified')) }} = 'observed'
        and {{ literal(var('ads_source_coverage_state', 'unverified')) }} = 'verified_complete'
        and match({{ literal(var('ads_coverage_receipt_sha256', '')) }}, '^[a-f0-9]{64}$'))
    ),
      'coverage_unverified',
    not window_mature, 'pending',
    dws_label_state = 'POSITIVE' and sourced_positive_count > 0, 'mature_positive',
    dws_label_state = 'OBSERVED_NEGATIVE' and sourced_positive_count > 0, 'label_conflict',
    dws_label_state = 'OBSERVED_NEGATIVE', 'mature_negative',
    dws_label_state = 'POSITIVE', 'positive_source_unavailable',
    'not_evaluable'
  ) as evaluation_state
  from evidence
)
select authority_id, tenant_id, subject_id, request_id, impression_id, item_id, content_revision,
       impression_time, window_end, impression_available_at, served_event_id, served_available_at,
       impression_source_partition, impression_source_sequence,
       served_source_partition, served_source_sequence,
       sourced_positive_count,
       if(sourced_positive_count > 0, first_positive_available_at, NULL) as positive_available_at,
       qualification, dws_label_state, window_mature, label_revision, evaluation_state,
       multiIf(evaluation_state = 'mature_positive', toNullable(toUInt8(1)),
               evaluation_state = 'mature_negative', toNullable(toUInt8(0)),
               CAST(NULL, 'Nullable(UInt8)')) as mature_label,
       'unassigned' as cohort, 'missing_authoritative_assignment' as assignment_state,
       {{ literal(var('ads_definition_revision', 'ads-exposure-r1')) }} as definition_revision,
       {{ literal(var('ads_source_kind', 'unverified')) }} as source_kind,
       {{ literal(var('ads_source_coverage_state', 'unverified')) }} as source_coverage_state,
       {{ literal(var('ads_coverage_receipt_sha256', '')) }} as coverage_receipt_sha256,
       parseDateTime64BestEffort({{ literal(var('ads_evaluation_cutoff', var('event_watermark'))) }}, 6, 'UTC') as data_as_of
from classified
