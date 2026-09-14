select impression_id from {{ ref('dws_impression_features') }}
group by impression_id having count() > 1
