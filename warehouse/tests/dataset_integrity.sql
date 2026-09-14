select sample_id from {{ ref('ds_recommendation_interaction') }}
where split = 'out_of_range' or label_state not in ('POSITIVE', 'OBSERVED_NEGATIVE')
   or feature_contract_id != {{ literal(var('feature_contract_id')) }}
   or not isFinite(user_interest) or not isFinite(item_quality)
union all
select sample_id from {{ ref('ds_recommendation_interaction') }}
group by sample_id having count() > 1
union all
select sample_id from {{ ref('ds_recommendation_interaction') }}
where request_time > feature_cutoff or feature_cutoff > impression_time
   or feature_available_at > feature_cutoff
   or label_observation_end != impression_time + toIntervalSecond({{ var('label_window_seconds') }})
   or label_observation_end > parseDateTime64BestEffort({{ literal(var('event_watermark')) }}, 6, 'UTC')
   {% for split, bounds in var('splits').items() %}
   or (split = {{ literal(split) }} and label_observation_end > parseDateTime64BestEffort({{ literal(bounds['end']) }}, 6, 'UTC'))
   {% endfor %}
