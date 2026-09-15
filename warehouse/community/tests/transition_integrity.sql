select current.event_id
from {{ ref('dwd_community_transition') }} current
left join {{ ref('dwd_community_transition') }} previous
  on current.producer = previous.producer
 and current.predecessor_event_id = previous.event_id
where
  (current.predecessor_event_id is not null and (
    previous.event_id is null
    or previous.source_offset >= current.source_offset
    or previous.issuer != current.issuer
    or previous.subject_id != current.subject_id
    or previous.target_type != current.target_type
    or previous.target_id != current.target_id
    or ifNull(previous.comment_id, '') != ifNull(current.comment_id, '')
  ))
  or (current.event_type = 'community.comment.created' and current.predecessor_event_id is not null)
  or (current.event_type = 'community.comment.deleted' and (
    current.predecessor_event_id is null or previous.event_type != 'community.comment.created'
  ))
  or (current.operation in ('like', 'dislike') and current.old_state = 0 and current.predecessor_event_id is not null)
  or (current.operation in ('unlike', 'undislike') and current.predecessor_event_id is null)
