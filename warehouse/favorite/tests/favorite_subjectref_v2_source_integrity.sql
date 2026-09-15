{{ config(enabled=var('favorite_subjectref_v2_read', false)) }}
with old as (select * from {{ source('favorite_landing', 'ods_favorite_event') }}
  where producer='rtw.community.favorite' and source_offset<= {{ var('favorite_subjectref_v2_through_offset', 0) }}),
new as (select * from {{ source('favorite_landing', 'ods_favorite_event_subjectref_v2_r1') }}
  where producer='rtw.community.favorite' and source_offset<= {{ var('favorite_subjectref_v2_through_offset', 0) }}),
conflicts as (
  select n.producer,n.source_offset,n.event_id from new n
  left join old o on o.producer=n.producer and o.source_offset=n.source_offset
    and o.event_id=n.event_id
  where o.event_id is null or n.issuer!='rtw.identity'
    or n.authority_id!='rtw.identity' or n.tenant_id!='platform'
    or n.subject_uid!=n.subject_id
    or toInt64OrNull(n.subject_uid) is null
    or toInt64OrNull(n.subject_uid)<=0
    or toString(toInt64OrNull(n.subject_uid))!=n.subject_uid
    or not match(n.origin_ods_sha256,'^[0-9a-f]{64}$')
    or n.origin_ods_sha256!='{{ var('favorite_subjectref_v2_old_ods_sha256', '') }}'
    or n.event_type!=o.event_type or n.favorite_id!=o.favorite_id
    or n.folder_id!=o.folder_id or n.target_type!=o.target_type
    or n.target_id!=o.target_id
    or isNull(n.target_revision)!=isNull(o.target_revision)
    or (not isNull(n.target_revision) and not isNull(o.target_revision)
      and n.target_revision!=o.target_revision)
    or n.operation!=o.operation or n.predecessor_event_id!=o.predecessor_event_id
    or n.event_time!=o.event_time or n.available_at!=o.available_at
    or n.dc_received_at!=o.dc_received_at
    or n.source_event_hash!=o.source_event_hash
    or n.technical_receipt!=o.technical_receipt or n.event_spec!=o.event_spec
    or n.authority_id!=o.authority_id or n.tenant_id!=o.tenant_id
    or n.subject_id!=o.subject_id
  union all
  select o.producer,o.source_offset,o.event_id from old o
  left join new n on n.producer=o.producer and n.source_offset=o.source_offset
    and n.event_id=o.event_id
  where n.event_id is null
  union all
  select producer,source_offset,event_id from old
  where authority_id!='rtw.identity' or tenant_id!='platform'
    or toInt64OrNull(subject_id) is null or toInt64OrNull(subject_id)<=0
    or toString(toInt64OrNull(subject_id))!=subject_id
  union all
  select producer,source_offset,min(event_id) from new
  group by producer,source_offset
  having uniqExact(tuple(event_id,source_event_hash,event_spec,technical_receipt,origin_ods_sha256))>1
  union all
  select producer,min(source_offset),event_id from new
  group by producer,event_id
  having uniqExact(tuple(source_offset,source_event_hash,event_spec,technical_receipt,origin_ods_sha256))>1
  union all
  select producer,source_offset,min(event_id) from old
  group by producer,source_offset
  having count()>1
  union all
  select 'rtw.community.favorite' as producer,0 as source_offset,
    'incomplete_frozen_prefix' as event_id
  from (select count() as row_count,min(source_offset) as first_offset,
    max(source_offset) as last_offset from old)
  where {{ var('favorite_subjectref_v2_through_offset', 0) }} < 1
    or row_count != {{ var('favorite_subjectref_v2_through_offset', 0) }}
    or first_offset != 1 or last_offset != {{ var('favorite_subjectref_v2_through_offset', 0) }}
)
select * from conflicts
