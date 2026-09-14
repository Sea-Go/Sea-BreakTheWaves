select authority_id, tenant_id, subject_id, impression_id from {{ ref('dws_impression_features') }}
group by authority_id, tenant_id, subject_id, impression_id having count() > 1
