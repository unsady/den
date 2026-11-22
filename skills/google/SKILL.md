---
name: google
description: Read, sort, draft, and send the user's Gmail, read and change their Google Calendar, and find, read, and edit their Drive files, Google Docs, and Sheets, through the Google APIs with den fetch. Use for any task on their mail, calendar, or Google files.
---

# google

den holds the user's Google tokens and adds them to each request. You never see them, so never ask the user for a password or a token.

```bash
den fetch --secret google https://gmail.googleapis.com/gmail/v1/users/me/profile
```

When den answers that Google is not connected or not set up, tell the user to run `den connect google` on their computer. The user may have allowed only some of Gmail, Calendar, and Drive; den refuses a request to one they did not allow and says so, and they add it by running `den connect google` again. When Google answers `403` with `SERVICE_DISABLED`, the user has to enable that API (Gmail, Google Calendar, Google Drive, Google Docs, or Google Sheets API) in their Google Cloud project under APIs & Services → Library; reconnecting does not help. Google's other services are out of den's reach.

## What waits for the user

Reads go at once, and so do the changes den knows stay with the user: labels, archiving, the trash, and drafts in Gmail; events with no one else on them in the primary calendar, without notifications; and edits, new files, copies, moves, the trash, and comments without an `@` on files only the user can open. Everything else waits for the user in Telegram: mail going out (`messages/send`, `drafts/send`), events with other people on them or in another calendar, notifications (`sendUpdates`, `sendNotifications`), cancelling, any delete, files other people can open or that belong to someone else, a comment with an `@`, since a mention emails that person, and any change den does not know, such as sharing or publishing a file. The user sees the mail's recipients, subject, and text. A draft goes only as the user saw it: change it after asking and den does not send it. den refuses batch requests and method-override headers; send each request on its own. Such a request returns at once, saying den asked: den sends it once the user allows it and tells you the service's answer in a new `[den:` message. The user already sees den's question, so do not tell them you asked, and do not send it again.

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
printf 'To: Name <name@example.com>\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n' "$subject" "$text" > mail.eml
den fetch --secret google -H 'Content-Type: message/rfc822' -d @mail.eml "https://www.googleapis.com/upload/gmail/v1/users/me/drafts?uploadType=media"
den fetch --secret google -H 'Content-Type: message/rfc822' -d @mail.eml "https://www.googleapis.com/upload/gmail/v1/users/me/messages/send?uploadType=media"
den fetch --secret google -d '{"id":"<draftId>"}' "$g/drafts/send"
```

- A reply keeps its thread: add `In-Reply-To:` and `References:` with the original's `Message-ID`, keep the subject with `Re: `, and send JSON with the thread id instead of the upload: `jq -n --arg raw "$(base64 -w0 mail.eml | tr '+/' '-_')" --arg t <threadId> '{raw: $raw, threadId: $t}' | den fetch --secret google -d @- "$g/messages/send"`.
- Write the body as plain UTF-8 text. Only the subject is encoded; a body in base64 without `Content-Transfer-Encoding: base64` reaches the reader as base64.
- Leave out `From:`; Gmail uses the account's address.

## Calendar

```bash
c=https://www.googleapis.com/calendar/v3/calendars/primary
den fetch --secret google "$c/events?singleEvents=true&orderBy=startTime&timeMin=$(date -u +%FT%TZ)&maxResults=20" | jq -r '.items[] | "\(.start.dateTime // .start.date)  \(.summary)"'
den fetch --secret google -d '{"summary":"Gym","start":{"dateTime":"2026-11-10T18:00:00+02:00"},"end":{"dateTime":"2026-11-10T19:00:00+02:00"}}' "$c/events"
den fetch --secret google -X PATCH -d '{"summary":"…"}' "$c/events/<eventId>"
```

- Use the user's time zone from `notes/` or ask once; write times with their offset.
- Use `primary` for the user's calendar. Another calendar is reachable by an id the user gives, such as a shared calendar's address, but each change in it waits for the user. Listing calendars (`calendarList`) is outside den's access.
- Inviting people: add `"attendees":[{"email":"…"}]` and `?sendUpdates=all`. den asks the user first.

## Drive, Docs, and Sheets

```bash
d=https://www.googleapis.com/drive/v3
den fetch --secret google "$d/files?q=name+contains+'budget'+and+trashed=false&fields=files(id,name,mimeType,modifiedTime)" | jq -r '.files[] | "\(.id)  \(.mimeType)  \(.name)"'
den fetch --secret google "$d/files/<id>/export?mimeType=text/plain"
den fetch --secret google https://docs.googleapis.com/v1/documents/<id> > doc.json
den fetch --secret google -d '{"requests":[{"insertText":{"endOfSegmentLocation":{},"text":"\nNew line"}}]}' https://docs.googleapis.com/v1/documents/<id>:batchUpdate
den fetch --secret google "https://sheets.googleapis.com/v4/spreadsheets/<id>/values/Sheet1!A1:D20"
den fetch --secret google -X PUT -d '{"values":[["Rent",1200]]}' "https://sheets.googleapis.com/v4/spreadsheets/<id>/values/Sheet1!A2:B2?valueInputOption=USER_ENTERED"
den fetch --secret google -d '{"values":[["Gym",40]]}' "https://sheets.googleapis.com/v4/spreadsheets/<id>/values/Sheet1!A:B:append?valueInputOption=USER_ENTERED"
```

- `q` takes Drive's search syntax: `name contains '…'`, `fullText contains '…'`, `mimeType='application/vnd.google-apps.document'` (Docs) or `…spreadsheet` (Sheets), `'<folderId>' in parents`, `modifiedTime > '2026-01-31T00:00:00'`. URL-encode it.
- A doc's or sheet's id is in its link: `docs.google.com/document/d/<id>/…`.
- For Docs, read the document first: `batchUpdate` places text by index (`.body.content[].endIndex`), and `replaceAllText` changes text without one. Send edits from the end of the doc backwards, so earlier indexes stay right.
- A new doc or sheet: `POST https://docs.googleapis.com/v1/documents` with `{"title":"…"}`, or `POST https://sheets.googleapis.com/v4/spreadsheets` with `{"properties":{"title":"…"}}`. Send the user its link.
- Move to the trash with `PATCH $d/files/<id>` and `{"trashed":true}`; the user can restore it.
- Upload with `uploadType=media` or `resumable`, never `multipart`: den cannot read which folder a multipart upload goes to, so it asks the user.

## Local findings

Findings about this skill on this machine are in `notes/skills/google.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
