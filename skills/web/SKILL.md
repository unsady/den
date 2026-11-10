---
name: web
description: Search the web with `den search` (the web_search tool does not work here) and read pages, including ones that need JavaScript. Use when a task needs information from the internet or a page's content.
---

# web

## Read a page

1. Use the `web_fetch` tool first, for any page, including links you found yourself. It is the fastest, and it turns the HTML into clean text, which `curl` piped through `sed` does worse. `curl` is for JSON APIs, files to save, and requests that need headers.
2. When it returns little or no text, the page likely builds itself with JavaScript. Load it in Lightpanda, a small headless browser:

   ```bash
   lightpanda fetch --dump markdown --strip-mode clutter --dump-max-bytes 40000 "<url>" 2>/dev/null
   ```

   - `--strip-mode clutter` keeps the main content, like a reader mode. Drop it when the part you need goes missing.
   - `--wait-selector "<css>"` waits for content that loads late, and `--dump-selector "<css>"` dumps only that element.
   - `--dump structured_data` gives a page's title, description, and JSON-LD as JSON.
   - Several URLs at once need `--json`.
   - A page takes about a second and under 100 MB of memory.

3. When both return a login wall, a captcha, or a bot check, tell the user what blocked you. Do not try to get around it. If the site has an official API, say so; the user can connect it.

## Search

```bash
den search "lightpanda browser memory use"
den search -n 15 --json "<query>"
```

den asks one search service after another until one answers: free ones without a key, and first any the user added a key for. Search in the language of the content you want, and read the best two or three results with `web_fetch` before you answer.

Do not use the `web_search` tool: it needs a gateway den does not have. If search keeps failing, tell the user; a key for Exa, DeepSeek, Tavily, or Brave, added as the secret `exa_key`, `deepseek_key`, `tavily_key`, or `brave_key`, gives higher limits.

## Local findings

Findings about this skill on this machine are in `notes/skills/web.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
