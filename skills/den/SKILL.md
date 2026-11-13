---
name: den
description: Schedule jobs and reminders, send the user a notification or a file, call GitHub, Google, or an API the user added with a token den holds, or look up an earlier conversation, with the den CLI.
---

# den

`den` reaches beyond the current conversation. Run `den help` for the full usage.

## Notify the user

```bash
den notify "The flight to Lisbon moved to 14:20"
```

The message goes to the user's Telegram. Send one only when the user asked for it or a job needs to report something. If it fails because Telegram is not configured, tell the user; do not retry.

## Send the user a file

```bash
den send invoice.pdf
den send --caption "Sales by month" chart.png
```

An image goes as a photo, anything else as a file, up to 50 MB. Send a file when the user asked for one or it says more than text would, such as a chart, a table, or a document. In the chat, tell the user in your reply what you sent.

## Schedule a job

A job runs fx later in a fresh session in this folder, with den's rules and the space's memory but without the current chat. Its final reply is sent to the user, unless the reply is exactly `SILENT` or the job is `--silent`.

```bash
den job add --schedule "in 30m" "Remind the user to call the bank."
den job add --name morning --schedule "0 9 * * 1-5" "Check notes/todo.md and send the user today's three most important items."
den job ls
den job log morning
den job run morning
den job rm morning
```

Schedules: `in 30m` or `in 2h` (once), `2026-11-10 09:00` (once, local time), `every 2h`, a cron expression such as `0 9 * * 1-5`, or `@daily`.

A job runs on the chat's main model, or on the second model the user chose in `/model` with `--slot second` or `den job slot morning second`. Which model is second, faster or stronger, is the user's choice: when they ask for a job on another model, use the second slot and tell them which model it is now, as `/status` shows. Without a second model, a job on it runs on the main one.

Write the prompt so it stands on its own. The job will not see this conversation, so name the files, the facts, and what to send. For a reminder, the prompt can simply say what to remind the user of.

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

den holds the user's tokens and adds them to requests, so you can use a service without seeing its key. A request that writes somewhere other people see, or cannot be undone, waits until the user allows it in Telegram. The skill for each service, `github` and `google` (Gmail and Calendar), says what it covers. Mail with an app password goes through den's local IMAP and SMTP instead; the `mail` skill covers it.

The user can also add their own APIs. `den service ls` lists them with their address, which requests wait for the user, and the user's note on what each API does. Call one by its name, only at its address:

```bash
den service ls
den fetch --secret news https://news.example.com/api/events
den fetch --secret news -d @event.json https://news.example.com/api/events
```

When the user asks for an API that is not listed, tell them to add it on their computer with `den service add <name> <https-address>`, which asks for the token. Never ask for the token in the chat. Keep what you learn about an API, such as its endpoints, in `notes/skills/den.md`.

## Local findings

Findings about this skill on this machine are in `notes/skills/den.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
