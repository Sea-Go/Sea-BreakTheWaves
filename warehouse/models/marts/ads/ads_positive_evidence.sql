-- Keep the positive evidence at one full-SubjectRef impression grain before
-- joining it to visible exposures. Availability and source identity must be
-- established independently of the DWS label's numeric value.
select i.authority_id as authority_id, i.tenant_id as tenant_id,
       i.subject_id as subject_id, i.impression_id as impression_id,
       countIf(
         b.action = 'effective_read'
         and b.event_time >= i.event_time
         and b.event_time < i.event_time + toIntervalSecond({{ var('label_window_seconds') }})
         and b.available_at <= parseDateTime64BestEffort({{ literal(var('ads_evaluation_cutoff', var('event_watermark'))) }}, 6, 'UTC')
         and be.source_partition != '' and be.source_sequence > 0
       ) as sourced_positive_count,
       minIf(b.available_at,
         b.action = 'effective_read'
         and b.event_time >= i.event_time
         and b.event_time < i.event_time + toIntervalSecond({{ var('label_window_seconds') }})
         and b.available_at <= parseDateTime64BestEffort({{ literal(var('ads_evaluation_cutoff', var('event_watermark'))) }}, 6, 'UTC')
         and be.source_partition != '' and be.source_sequence > 0
       ) as first_positive_available_at
from {{ ref('dwd_impressions') }} i
left join {{ ref('dwd_behaviors') }} b on {{ subject_equals('i', 'b') }}
  and i.impression_id = b.impression_id and i.request_id = b.request_id
  and i.item_id = b.item_id and i.content_revision = b.content_revision
left join {{ ref('dwd_events') }} be on {{ subject_equals('b', 'be') }}
  and b.event_id = be.event_id and be.event_type = 'behavior'
group by i.authority_id, i.tenant_id, i.subject_id, i.impression_id
