select *
from {{ source('landing', 'event_history') }}
where batch_id in (
  {% for batch in var('source_batches') %}
    {{ literal(batch) }}{% if not loop.last %},{% endif %}
  {% endfor %}
)
and available_at <= parseDateTime64BestEffort({{ literal(var('observed_until')) }}, 6, 'UTC')
