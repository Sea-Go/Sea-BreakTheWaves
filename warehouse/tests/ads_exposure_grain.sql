select authority_id, tenant_id, subject_id, impression_id
from {{ ref('ads_recommendation_exposures') }}
group by authority_id, tenant_id, subject_id, impression_id
having count() != 1
