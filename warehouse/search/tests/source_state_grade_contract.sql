select event_id
from {{ ref('ods_qrel_events') }}
where (status = 'active' and (relevance_grade is null or judged_mask = false))
   or (status = 'retracted' and (relevance_grade is not null or judged_mask = true))
   or status not in ('active', 'retracted')
