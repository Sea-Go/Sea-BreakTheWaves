{% macro subject_equals_v2(left, right) -%}
{{ left }}.issuer = {{ right }}.issuer
and {{ left }}.subject_uid = {{ right }}.subject_uid
{%- endmacro %}
