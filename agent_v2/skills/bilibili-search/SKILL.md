# Bilibili Search Skill

Search Bilibili video results for travel guide material.

The Go tool calls `scripts/bilibili-search.py` and expects JSON with normalized
video fields. The script uses `bilibili-api-python`; install it in the Python
environment that runs the agent:

```bash
pip install bilibili-api-python
```

Optional environment:

- `BILIBILI_COOKIE`: raw Bilibili cookie string, useful when public search is
  rate limited or when richer data is needed.
