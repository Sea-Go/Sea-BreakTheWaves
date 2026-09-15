select producer, source_offset
from {{ ref('dwd_community_transition') }}
group by producer, source_offset
having count() != 1
