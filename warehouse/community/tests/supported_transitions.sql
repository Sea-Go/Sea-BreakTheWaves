select event_id
from {{ ref('dwd_community_transition') }}
where transition_type not in
  ('comment_create','comment_delete','comment_like','comment_unlike','comment_dislike','comment_undislike',
   'target_like','target_unlike')
   or producer not in ('rtw.comment-rpc','rtw.like-mq')
   or event_type not in
     ('community.comment.created','community.comment.deleted','community.comment.interaction','community.target.interaction')
