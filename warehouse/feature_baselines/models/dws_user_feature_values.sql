-- H10.c: one frozen row per approved fact feature. The full reversible
-- contribution ledger and complete source prefixes are separate artifacts.
{% for feature in var('feature_spec')['features'] %}
{% if not loop.first %}UNION ALL{% endif %}
SELECT
  {{ literal(feature['name']) }} AS name,
  {% if feature['mode'] == 'count' %}
  toString(count()) AS value,
  toUInt8(count() = 0) AS missing,
  toUInt8(false) AS oov
  {% else %}
  if(count() = 0, {{ literal(feature['default']) }},
     if(has([{% for word in feature['vocabulary'] %}{{ literal(word) }}{% if not loop.last %},{% endif %}{% endfor %}],
            argMax(value_ref, tuple(occurred_at, producer, event_id))),
        argMax(value_ref, tuple(occurred_at, producer, event_id)), {{ literal(feature['oov']) }})) AS value,
  toUInt8(count() = 0) AS missing,
  toUInt8(count() > 0 AND NOT has([{% for word in feature['vocabulary'] %}{{ literal(word) }}{% if not loop.last %},{% endif %}{% endfor %}],
                         argMax(value_ref, tuple(occurred_at, producer, event_id)))) AS oov
  {% endif %}
FROM {{ var('landing_table') }}
WHERE semantic_kind = {{ literal(feature['kind']) }}
  AND predicate = {{ literal(feature['predicate']) }}
  AND occurred_at <= parseDateTime64BestEffort({{ literal(var('as_of')) }}, 6, 'UTC')
  AND observed_at <= parseDateTime64BestEffort({{ literal(var('available_at')) }}, 6, 'UTC')
  {% if feature.get('window_seconds', 0) > 0 %}
  AND occurred_at > parseDateTime64BestEffort({{ literal(var('as_of')) }}, 6, 'UTC') - INTERVAL {{ feature['window_seconds'] }} SECOND
  {% endif %}
{% endfor %}
