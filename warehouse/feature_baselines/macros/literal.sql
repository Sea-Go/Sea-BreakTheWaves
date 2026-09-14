{% macro literal(value) -%}
'{{ value | replace("'", "''") }}'
{%- endmacro %}
