---
name: model-stats
description: Report how the model providers performed - silence before the first token, stalls mid-answer, writing speed (tokens per second), failures and rate limits - and find slowdowns by hour. Use when the user asks about TPS, lag, latency, whether a provider or model is slow today, or for model statistics.
---

# model-stats

den's model proxy measures every request fx sends to a model provider. Ask den for them:

```bash
den stats                          # summary of the last 24 hours
den stats --since 2h
den stats --since 168h --json      # one JSON line per call, for your own analysis
```

Each call has:

- `at`, `provider`, `model`, `status`: HTTP status; 400 and above failed, 429 is a rate limit, 402 a used-up allowance.
- `first_ms`: how long the provider was silent before the first token. Reasoning counts as a token, so this is the wait the user feels before anything happens. 0 when no token came.
- `gap_ms`: the longest silence after the first token, a stall mid-answer.
- `total_ms`, `in_tokens`, `out_tokens`. The writing speed is `out_tokens / (total_ms - first_ms) * 1000` tokens per second. `out_tokens` includes reasoning.
- `error`: the start of a failed answer, or why den could not reach the provider.

One turn makes several calls, one per model step, and fx retries a failed call as a new one.

## Finding a slowdown

Compare a recent window with the days before, per provider and model, rather than reading single calls. Short answers have a high share of fixed delay, so judge speed on calls with at least 200 output tokens. A slowdown shows as a higher median `first_ms` or lower speed for a few hours, stalls of tens of seconds, or a run of 429 and 5xx.

```bash
den stats --since 168h --json | jq -s '
  map(select(.status < 400 and .first_ms > 0 and .out_tokens >= 200))
  | group_by(.provider + " " + .model + " " + (.at[:13]))
  | map({key: (.[0].provider + " " + .[0].model), hour: .[0].at[:13], calls: length,
         first_s: (map(.first_ms) | sort | .[length/2|floor] / 1000),
         tok_s: (map(.out_tokens * 1000 / (.total_ms - .first_ms)) | sort | .[length/2|floor] | floor)})'
```

`at` is in den's time zone, the user's, with its offset, so `.at[:13]` is the user's hour.

## Before den measured

den keeps about the last 2000 calls and measured nothing before it started to. For older history, fx's session logs have one `model_call` line per call with `elapsed_ms`, `input_tokens`, `output_tokens`, `outcome`, `failure`, and `retry_after_s`, but no time to the first token:

```bash
cat ~/.fx/sessions/v2/*/log.jsonl | jq -c 'select(.type == "model_call") | .data'
```

There `output_tokens / elapsed_ms` mixes in the wait before the first token, so it reads lower than the writing speed.

## Reporting

Lead with what the user asked, in numbers: "deepseek-v4.1-flash today: first token in 1.4 s (median), 98 tok/s; from 14:00 to 15:00 first token 9 s and two 30 s stalls; 3 rate limits." Say how many calls a number rests on when it is few.

## Local findings

Findings about this skill on this machine are in `notes/skills/model-stats.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
