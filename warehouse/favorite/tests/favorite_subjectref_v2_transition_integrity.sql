{{ config(enabled=var('favorite_subjectref_v2_read', false)) }}
select r.event_id from {{ ref('dwd_favorite_transition_subjectref_v2_r1') }} r
left join {{ ref('dwd_favorite_transition_subjectref_v2_r1') }} a
  on r.producer=a.producer and r.predecessor_event_id=a.event_id
where (r.operation='retract' and (
  a.event_id is null or a.operation!='assert' or a.source_offset>=r.source_offset
  or not ({{ subject_equals_v2('r', 'a') }})
  or a.favorite_id!=r.favorite_id or a.folder_id!=r.folder_id
  or a.target_type!=r.target_type
  or a.target_id!=r.target_id
  or isNull(a.target_revision)!=isNull(r.target_revision)
  or (not isNull(a.target_revision) and not isNull(r.target_revision)
    and a.target_revision!=r.target_revision)
)) or (r.operation='assert' and r.predecessor_event_id!='')
