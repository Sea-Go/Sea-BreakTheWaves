select event_id
from {{ ref('dwd_community_transition') }}
where issuer != 'rtw.identity'
   or not match(subject_id, '^[1-9][0-9]*$')
   or target_revision is not null
   or revision_status != 'unknown'
   or (producer = 'rtw.comment-rpc' and (search_evidence is null or search_evidence != false))
   or (producer = 'rtw.like-mq' and search_evidence is not null)
