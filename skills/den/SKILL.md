---
name: den
description: Schedule jobs and reminders, send the user a file, call GitHub, Google, or an API the user added with a token den holds, help the user connect a service or set den up, or look up an earlier conversation, with the den CLI.
---

# den

`den` reaches beyond the current conversation. Run `den help` for the full usage.

## Send the user a file

```bash
den send invoice.pdf
den send --caption "Sales by month" --widget chart.json chart.png
```

An image goes as a photo, anything else as a file, up to 50 MB. `--widget` gives the den app the chart's data, which it draws itself instead of the picture; the `charts` skill says how. Send a file when the user asked for one or it says more than text would, such as a chart, a table, or a document. In the chat, the files you send arrive under your reply when the turn ends, pictures together as one album, so they are below it, not above, and do not repeat in the reply what a caption says.

## Schedule a job

A job runs fx later in a fresh session in this folder, with den's rules and the space's memory but without the current chat. Its final reply is sent to the user, unless the run ends with `den quiet` or the job is `--silent`. Write its prompt in plain words, such as "send nothing when it succeeds": den tells the job how to stay quiet.

```bash
den job add --name call-the-bank --title "Позвонить в банк" --schedule "in 30m" "Remind the user to call the bank."
den job add --name morning-brief --title "Утренняя сводка" --schedule "0 9 * * 1-5" "Check notes/todo.md and send the user today's three most important items."
den job ls
den job log morning-brief
den job run morning-brief
den job rm morning-brief
```

Every job, a reminder too, needs both:

- `--name`: 2 or 3 lowercase English words joined by `-`, without digits, that say what the job does, such as `morning-brief`. It is how you and den refer to the job, and it must be new.
- `--title`: a few words in the user's language, which the user sees above the job's messages and in `/jobs`.

Schedules: `in 30m` or `in 2h` (once), `2026-11-10 09:00` (once, local time), `every 2h`, a cron expression such as `0 9 * * 1-5`, or `@daily`.

A job runs on the chat's main model, or on the second model the user chose in `/model` with `--slot second` or `den job slot morning-brief second`. Which model is second, faster or stronger, is the user's choice: when they ask for a job on another model, use the second slot and tell them which model it is now, as `/status` shows. Without a second model, a job on it runs on the main one.

Write the prompt so it stands on its own. The job will not see this conversation, so name the files, the facts, and what to send. For a reminder, the prompt can simply say what to remind the user of.

For a recurring task that needs more than a few lines of rules, write the rules in a skill and let the prompt only name it and its mode, such as `Use the news skill in daily mode.` Rules in a prompt cannot be fixed later, and they win over a skill that says otherwise. To change such a job's prompt, remove the job and add it again with the same name, schedule, and slot.

Confirm with the user before you add a recurring job they did not ask for. After `den job add`, tell the user when it runs next.

## Look up an earlier conversation

Past sessions are exported as markdown to `$DEN_ARCHIVE`, one file per session named `YYYY/MM/DD-HHMM-title-id.md`. Refresh the export first, then search:

```bash
den sessions export
rg -il "invoice" "$DEN_ARCHIVE"
rg -i -C3 "invoice" "$DEN_ARCHIVE/2026/11"
```

Search with word stems for inflected languages, such as `rg -i "rechnung|rechnungen"`. List matching files first, then read the one or two that fit by date and title. Do this only when the user refers to an earlier conversation.

## Use a service with a secret

```bash
den fetch --secret github https://api.github.com/user
```

den holds the user's tokens and adds them to requests, so you can use a service without seeing its key. A request that writes somewhere other people see, or cannot be undone, waits until the user allows it in Telegram. The skill for each service, `github` and `google` (Gmail, Calendar, Drive, Docs, Sheets), says what it covers. Mail with an app password goes through den's local IMAP and SMTP instead; the `mail` skill covers it.

The user can also add their own APIs. `den service ls` lists them with their address, which requests wait for the user, and the user's note on what each API does. Call one by its name, only at its address:

```bash
den service ls
den fetch --secret news https://news.example.com/api/events
den fetch --secret news -d @event.json https://news.example.com/api/events
```

Keep what you learn about an API, such as its endpoints, in `notes/skills/den.md`.

## Help the user connect something

The user connects services and changes settings in a terminal on their computer, where den runs, never in the chat. When they ask what they can connect, or how, answer with the command to run, in a ``` block they can copy. Never ask for a token, password, or key in the chat: the command asks for it.

| What | Command on their computer |
|---|---|
| Any mailbox, with an app password | `den connect mail` |
| Gmail, Calendar, and Drive with Docs and Sheets | `den connect google`; they first need an OAuth client from their own Google Cloud project, as the guide says |
| GitHub | a fine-grained token for the repositories you may use, then `den secret set github_token` |
| Their own API | `den service add` |
| Another model provider or a subscription | `den login <provider>`, or `den connect codex`, `grok`, `claude` |
| Voice messages | `DEN_WHISPER_MODEL` in `env` in `~/.config/den/config.json`, then `den restart` |

For their own API, find its docs, then write the whole command for them, with a short name, the API's https address, the header if it is not `Authorization: Bearer`, and an `--about` with what it does, its main endpoints, and its docs' address:

```bash
den service add --header X-Api-Key --about "Tasks API: GET /tasks, POST /tasks; docs at https://api.example.com/docs" tasks https://api.example.com
```

Say where they get the token. `--ask deletes`, `never`, or `always` changes which requests wait for them; the default asks before writes. den takes only an https API with a token in one header: an API that needs OAuth, a login and password, the token in the address, or plain http on their own network cannot be added this way, so say so rather than guess. The guides are at https://github.com/unsady/den/tree/main/docs.

## Local findings

Findings about this skill on this machine are in `notes/skills/den.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
