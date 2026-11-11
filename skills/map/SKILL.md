---
name: map
description: Classify, score, or extract fields from many similar items, such as job ads, emails, news, reviews, or transactions, by asking a cheap model about each item in its own short request with den map. Use instead of reading the items yourself when there are more than about 20 and each can be judged on its own.
---

# map

Reading a hundred items in your own session is slow and costly: every step resends all of them, and judgment slips as the context fills. `den map` asks a cheap model about each item in a separate request with the same instructions, a few at a time, so a hundred items take minutes and cents.

Use it when each item can be judged from its own text. Keep the work yourself when an item needs browsing, several steps, or comparison with the others.

## 1. Write the items

One JSON value per line in `~/space/.data/`, with an `id` and only what the judgment needs: cut markup and fields that do not matter.

```bash
jq -c '.records[] | {id, title, company, location, text: (.description | .[0:8000])}' jobs.json > .data/jobs.jsonl
```

## 2. Write the instructions

Say what an item is, the criteria, and the exact JSON to answer with, with a small set of allowed values for any verdict. Put the hard cases you already know as short examples. When a wrong answer is costly, ask for a short exact quote from the item as evidence, and let unclear items take a value such as `uncertain` instead of a guess.

```text
You screen one job ad for a senior TypeScript developer who works remotely from Serbia.
Answer {"verdict": "fit|unclear|reject", "quote": "<exact words from the ad that decide it>", "reason": "<one sentence>"}.
Reject only when the ad itself rules the role out; missing details mean unclear.
```

## 3. Try a few, then run them all

```bash
head -5 .data/jobs.jsonl > .data/jobs-try.jsonl
den map -p @.data/jobs-prompt.md .data/jobs-try.jsonl
den map -p @.data/jobs-prompt.md .data/jobs.jsonl
```

Each run needs the user's Allow in Telegram, unless they allowed that model for good. When den asks, the command returns at once; den runs it once the user allows it and tells you its summary in a new `[den:` message, so do not run it again or wait for it. A run that goes at once may take minutes: if your shell returns while it still runs, wait on it again with a long wait, up to 300000 ms. When the user denies a run, tell them what you wanted to run instead of retrying. Leave `--effort` at the model's default: thinking is what keeps a model from rejecting on the first similar phrase. `--effort none` answers about twice as fast with no thinking, but on job ads it rejected several items the default kept, so use it only when the user wants speed over care, and check its rejections. `--workers` sets how many items run at once: 4 by default, at most 8.

Read the trial answers and fix the instructions before the full run. den prints how many items got each value, the tokens, and the cost, and writes one line per item to `<items>.out.jsonl`, with the answer under `answer`:

```bash
jq -c 'select(.answer.verdict == "fit") | {id, reason: .answer.reason}' .data/jobs.out.jsonl
```

If a run stops, `--resume` asks only about the items without an answer. The model is fx's own unless the user names one: `--model opencode-go/claude-haiku-5-5`. A ChatGPT subscription works when you name its model, such as `--model codex/gpt-6-luna`; take a small one, since every item spends the subscription. `fx models` lists the models.

## 4. Check before you trust it

Spot-check a few answers of each value against their items, and check any quotes are really in the item. Look at the items you would act on yourself, and tell the user how many items the model judged and how many you checked.
