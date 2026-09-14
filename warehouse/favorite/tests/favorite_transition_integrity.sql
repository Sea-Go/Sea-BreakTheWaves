select r.event_id
from {{ ref('dwd_favorite_transition') }} r
left join {{ ref('dwd_favorite_transition') }} a
  on r.producer=a.producer and r.predecessor_event_id=a.event_id
where (r.operation='retract' and (
   a.event_id is null or a.operation!='assert' or a.source_offset>=r.source_offset
   or a.authority_id!=r.authority_id or a.tenant_id!=r.tenant_id or a.subject_id!=r.subject_id
   or a.favorite_id!=r.favorite_id or a.target_type!=r.target_type or a.target_id!=r.target_id
   or ifNull(a.target_revision,'')!=ifNull(r.target_revision,'')
)) or (r.operation='assert' and r.predecessor_event_id!='')
