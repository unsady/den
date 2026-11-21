# Security

How den keeps your accounts safe, and what it does not stop.

```
  you, in Telegram
        │  messages, and Allow / Deny on requests
        ▼
  ┌────────────────────────── container ──────────────────────────┐
  │                                                               │
  │   den (root)                            agent (uid 1000)      │
  │   holds every secret                    fx with its tools     │
  │                                                               │
  │   model proxy :4100   ◀── model requests, no key ──────────── │
  │   git proxy   :4300   ◀── clone and push, no token ────────── │
  │   mail  :1143, :1025  ◀── IMAP and SMTP, no password ──────── │
  │   den fetch, den job  ◀── the den command, on a socket ────── │
  │        │                                                      │
  └────────┼──────────────────────────────────────────────────────┘
           │  den adds the key, only for the host it belongs to
           ▼
  model provider, GitHub, Google, your mail server, your APIs
```

- **Two users.** den runs as root and the agent as an ordinary user. The agent reaches den only through the `den` command on a socket, so it can ask den to send a message or a request, but it cannot read den's files.
- **Secrets stay with den.** You enter every secret in your terminal, never in the chat or on a command line. den keeps them in a volume only root can read, never in the environment, which every process could see.
- **Keys go only where they belong.** The agent talks to the model provider, GitHub, and your mail through den's local proxies, and calls other services with `den fetch`. den adds the key to each request and sends it only to the host it was saved for, so a page or a mail that tricks the agent cannot carry a key anywhere else.
- **Outward actions wait for you.** Sending mail, inviting people, pushing to a branch the agent did not create, deleting things, and writes to your own APIs show up in Telegram with Allow and Deny. A push that waits goes to GitHub under refs of den's own, so the question names its commits and files, and den moves the branch once you allow it and deletes those refs whatever you answer. Nothing is sent before Allow. The agent does not wait on the question: den keeps the action, takes it once you allow it, and tells the agent what came of it so it can carry on. A question nobody answers in a day expires. For a request you make often, such as a push to one branch or one kind of write to your API, **Always** allows that kind from then on. While the agent works through a task, **1 hour** allows every write to that API, or to that GitHub repository, but deletes and changes to its access, secrets, workflows, and branch rules, until the hour ends. Mail to one person offers the same two for that address; mail to several people and invitations are asked about every time.
- **A log of what went out.** `/log` lists what the agent changed outside in the last day, and how: without a question, allowed by you, by your rule, denied, or expired. It names the repeating jobs added or removed, too. It also lists what you always allow, with a button to take each back.
- **Only your chat.** The bot answers the one Telegram chat it was set up with.

## What den does not stop

Inside its container the agent works with full access, it can reach the internet, and it reads what you give it, such as your mail and notes. A prompt hidden in a page could make it send that content somewhere. Give it the accounts and repositories it needs, and no more. The model provider sees your conversations.

## The agent's folder

The agent's home is a folder on your computer, and the agent writes everything in it. Read it, but do not run git or other tools that follow a repository's own config there, such as an editor's git integration or a shell prompt that shows git status: a repository's config can name a program to run, and it would run as you, outside the container.
