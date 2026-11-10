---
name: google
description: Read, sort, draft, and send the user's Gmail, and read and change their Google Calendar, through the Google APIs with den fetch. Use for any task on their mail or calendar.
---

# google

den holds the user's Google tokens and adds them to each request. You never see them, so never ask the user for a password or a token.

```bash
den fetch --secret google https://gmail.googleapis.com/gmail/v1/users/me/profile
```

When den answers that Google is not connected or not set up, tell the user to run `den connect google` on their computer. When Google answers `403` with `SERVICE_DISABLED`, the user has to enable that API (Gmail API or Google Calendar API) in their Google Cloud project under APIs & Services → Library; reconnecting does not help. den can reach Gmail and Calendar only, not Drive or other services.

## What waits for the user

Reads, labels, archiving, trash, drafts, and events with no other people on them go at once. den asks the user in Telegram first before mail goes out (`messages/send`, `drafts/send`), before an event with other people on it is created or changed, before anyone is notified (`sendUpdates`), and before an event is deleted. The user sees the mail's recipients, subject, and text. Such a request waits up to 15 minutes. Tell the user what you are waiting for, and when den answers that they did not allow it, do not send it again.

Prefer a draft when the user did not ask you to send: they can read and send it from Gmail themselves.

## Mail

```bash
g=https://gmail.googleapis.com/gmail/v1/users/me
den fetch --secret google "$g/messages?q=is:unread+newer_than:2d&maxResults=20" | jq -r '.messages[]?.id'
den fetch --secret google "$g/messages/<id>?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date" | jq '{snippet, headers: .payload.headers}'
den fetch --secret google "$g/messages/<id>?format=full" > msg.json
den fetch --secret google -d '{"removeLabelIds":["UNREAD"]}' "$g/messages/<id>/modify"
den fetch --secret google -X POST "$g/messages/<id>/trash"
```

- `q` takes Gmail's search syntax: `from:`, `subject:`, `has:attachment`, `label:`, `after:2026/01/31`, `in:inbox`.
- A message's text is in `.payload.parts[]` (or `.payload.body`) as base64url `data`: decode with `tr '_-' '/+' | base64 -d`, padding with `=` if needed. Prefer the `text/plain` part.
- An attachment is `GET $g/messages/<id>/attachments/<attachmentId>`, its `.data` in base64url.
- Fetch many messages with `format=metadata` first, and the full ones you need after.

## Writing mail

Write the message as a file with CRLF line ends, then upload it as it is:

```bash
subject="=?UTF-8?B?$(printf %s "Résumé for review" | base64 -w0)?="
printf 'To: Name <name@example.com>\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n' "$subject" "$text" > mail.eml
den fetch --secret google -H 'Content-Type: message/rfc822' -d @mail.eml "https://www.googleapis.com/upload/gmail/v1/users/me/drafts?uploadType=media"
den fetch --secret google -H 'Content-Type: message/rfc822' -d @mail.eml "https://www.googleapis.com/upload/gmail/v1/users/me/messages/send?uploadType=media"
den fetch --secret google -d '{"id":"<draftId>"}' "$g/drafts/send"
```

- A reply keeps its thread: add `In-Reply-To:` and `References:` with the original's `Message-ID`, keep the subject with `Re: `, and send JSON with the thread id instead of the upload: `jq -n --arg raw "$(base64 -w0 mail.eml | tr '+/' '-_')" --arg t <threadId> '{raw: $raw, threadId: $t}' | den fetch --secret google -d @- "$g/messages/send"`.
- Leave out `From:`; Gmail uses the account's address.

## Calendar

```bash
c=https://www.googleapis.com/calendar/v3/calendars/primary
den fetch --secret google "$c/events?singleEvents=true&orderBy=startTime&timeMin=$(date -u +%FT%TZ)&maxResults=20" | jq -r '.items[] | "\(.start.dateTime // .start.date)  \(.summary)"'
den fetch --secret google -d '{"summary":"Gym","start":{"dateTime":"2026-11-10T18:00:00+02:00"},"end":{"dateTime":"2026-11-10T19:00:00+02:00"}}' "$c/events"
den fetch --secret google -X PATCH -d '{"summary":"…"}' "$c/events/<eventId>"
```

- Use the user's time zone from `notes/` or ask once; write times with their offset.
- Only the user's own calendars by id are reachable: `primary`, or an id the user gives, such as a shared calendar's address. Listing calendars (`calendarList`) is outside den's access.
- Inviting people: add `"attendees":[{"email":"…"}]` and `?sendUpdates=all`. den asks the user first.

## Local findings

Findings about this skill on this machine are in `notes/skills/google.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
