---
name: python
description: Run Python on demand with uv, for data work, calculations, file conversion, and charts. Use when a task needs a script, a library such as pandas, or a plot sent as an image.
---

# python

There is no Python in the image. uv fetches one when a script runs, and caches it in `~/.cache/uv`.

## Install uv

Once, if `command -v uv` finds nothing:

```bash
curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR="$HOME/.local/bin" UV_NO_MODIFY_PATH=1 sh
```

The first `uv run` downloads Python and the packages: tens of MB, under a minute. Later runs use the cache and start in a second.

## Run code

Put scripts in your space or in `/tmp`, never in skills. List every package after `--with`:

```bash
uv run --with pandas --with openpyxl script.py
uv run --with pandas python -c 'import pandas as pd; print(pd.read_csv("data.csv").describe())'
```

## Charts

Draw charts as the `charts` skill says: matplotlib with den's style, saved as a PNG and sent with `den send`.

## Local findings

Findings about this skill on this machine are in `notes/skills/python.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
