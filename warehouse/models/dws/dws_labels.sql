with evidence as (
  select i.authority_id as authority_id, i.tenant_id as tenant_id, i.subject_id as subject_id,
         i.impression_id as impression_id,
         countIf(b.action = 'effective_read'
           and b.event_time >= i.event_time
           and b.event_time < i.event_time + toIntervalSecond({{ var('label_window_seconds') }})
           and tuple(b.authority_id, b.tenant_id, b.subject_id) = tuple(i.authority_id, i.tenant_id, i.subject_id) and b.request_id = i.request_id
           and b.item_id = i.item_id and b.content_revision = i.content_revision
         ) as positive_events
  from {{ ref('dwd_impressions') }} i
  left join {{ ref('dwd_behaviors') }} b on {{ subject_equals('i', 'b') }} and i.impression_id = b.impression_id
  group by i.authority_id, i.tenant_id, i.subject_id, i.impression_id
)
select f.*, toUInt32({{ var('label_revision') }}) as label_revision,
       f.impression_time + toIntervalSecond({{ var('label_window_seconds') }}) as window_end,
       least(
         parseDateTime64BestEffort({{ literal(var('event_watermark')) }}, 6, 'UTC'),
         parseDateTime64BestEffort({{ literal(var('observed_until')) }}, 6, 'UTC')
       ) >= window_end as window_mature,
       multiIf(f.qualification != 'eligible', 'EXCLUDED',
               not window_mature, 'PENDING',
               e.positive_events > 0, 'POSITIVE', 'OBSERVED_NEGATIVE') as label_state,
       toUInt8(e.positive_events > 0) as label
from {{ ref('dws_impression_features') }} f
left join evidence e on {{ subject_equals('f', 'e') }} and f.impression_id = e.impression_id
