---
name: mail
description: Read, search, sort, and send the user's email over IMAP and SMTP, which den serves locally with the user's app password. Use for mail when the google skill's account is not connected, or for a mailbox that is not Gmail.
---

# mail

den logs in to the user's mailbox itself. You connect to den on this machine with any name and password, and never see the real one, so never ask the user for it.

- IMAP: `imap://127.0.0.1:1143`, no TLS
- SMTP: `smtp://127.0.0.1:1025`, no TLS

When den answers that it has no mail account, tell the user to run `den connect mail` on their computer, which asks for the address and an app password. For Gmail, an app password needs 2-Step Verification and is created at myaccount.google.com/apppasswords.

If the user connected Google with `den connect google`, prefer the `google` skill for Gmail: it also covers the calendar.

## What waits for the user

Reading and sorting go at once. Every message you send waits until the user allows it in Telegram, where they see the recipients, the subject, and the text. It waits up to 15 minutes. Tell the user what you are waiting for, and when den answers that it was not sent, do not send it again.

Prefer a draft when the user did not ask you to send: save it to the Drafts folder with `APPEND`.

## Reading

curl speaks IMAP. A custom command goes in `-X` and prints the server's answer.

```bash
m='imap://127.0.0.1:1143'
curl -s "$m/" -u den:den                                     # folders
curl -s "$m/INBOX" -u den:den -X 'UID SEARCH UNSEEN'
curl -s "$m/INBOX" -u den:den -X 'UID SEARCH SINCE 01-Nov-2026 FROM "anya"'
curl -s "$m/INBOX" -u den:den -X 'UID FETCH 120:130 (FLAGS BODY.PEEK[HEADER.FIELDS (FROM TO SUBJECT DATE)])'
curl -s "$m/INBOX" -u den:den -X 'UID FETCH 123 BODY.PEEK[]' > msg.eml
```

- `BODY.PEEK` leaves a message unread; plain `BODY[]` marks it read.
- Gmail also takes its own search syntax: `-X 'UID SEARCH X-GM-RAW "has:attachment newer_than:7d"'`.
- Folder names go into the URL encoded: Gmail's `[Gmail]/All Mail` is `$m/%5BGmail%5D%2FAll%20Mail`.
- A message's parts are MIME: decode base64 parts with `base64 -d` and quoted-printable ones with `perl -MMIME::QuotedPrint -pe '$_=decode_qp($_)'`.

## Sorting

```bash
curl -s "$m/INBOX" -u den:den -X 'UID STORE 123 +FLAGS (\Seen)'
curl -s "$m/INBOX" -u den:den -X 'UID MOVE 123 Archive'
```

Gmail archives with `UID MOVE 123 "[Gmail]/All Mail"`. Move to Trash instead of deleting; do not `EXPUNGE` without the user's word.

## Sending

Write the message with CRLF line ends, as in the google skill, then:

```bash
curl -s smtp://127.0.0.1:1025 --mail-from me --mail-rcpt name@example.com --upload-file mail.eml
```

- Each recipient, including Cc and Bcc, takes its own `--mail-rcpt`; Bcc goes only there, not in the headers.
- Leave out `From:` or set it to the user's address; den sends from their account.
- A reply keeps its thread with `In-Reply-To:` and `References:` set to the original's `Message-ID`.

## Local findings

Findings about this skill on this machine are in `notes/skills/mail.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
