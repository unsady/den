# den

A personal AI agent you talk to in Telegram. It runs on your own computer, in a container, and does real work: reads and answers mail, keeps your calendar, runs jobs on a schedule, pushes to GitHub, calls your own APIs, searches the web, and remembers what it learns.

den is built on [fx](https://github.com/vercel-labs/fx), a small and fast agent CLI, and adds what an assistant needs beyond coding:

- **Small.** Idle, den takes about 20 MB of memory. Nothing stays loaded between messages: whisper, Python, and the browser start only when a task needs them.
- **Safe with your accounts.** The agent never sees a password, token, or key. den holds them and adds them to the agent's requests itself, and anything that reaches other people or cannot be undone waits for your tap in Telegram.
- **Works out of the box.** One command installs it; the setup asks for a Telegram bot and a model key, and the agent is ready.

## What it does

- **Chat** from Telegram: text, photos, files, and voice messages, with a short live line of what the agent is doing.
- **Jobs and reminders**: "every morning at 9, send me today's three most important items". A job runs on its own and sends you its reply.
- **Mail and calendar** through Gmail and Google Calendar, or any mailbox with an app password.
- **GitHub**: the agent clones, commits, and opens pull requests in the repositories you allow.
- **Your own APIs**: give den a token for a service, and the agent can call it without seeing the token.
- **Web search** that works without keys, and reading pages that need JavaScript.
- **Files and charts**: the agent sends you documents, tables, and charts sized for a phone.
- **Memory**: the agent keeps notes and writes skills for tasks it has worked out. A nightly review rereads the day's conversations and updates both, and every change is a git commit you can read or revert.
- **Two models**: a main one for the chat and a second one, faster or stronger, for the jobs you pick.

## Install

You need a Telegram account, an API key for a model provider, and a tool that runs containers: Apple's [`container`](https://github.com/apple/container) or Docker on a Mac, Docker or Podman on Linux.

```bash
curl -fsSL https://raw.githubusercontent.com/unsady/den/main/install.sh | sh
```

The script puts `den` in `~/.local/bin`, checks its digest, and runs `den setup`. The setup gets den's image, starts the container, and asks for two things:

- A Telegram bot's token, from @BotFather. You then send the bot any message, and den takes your chat from it. The bot answers only that chat.
- The account that pays for the model: OpenCode Go, OpenRouter, DeepSeek, a ChatGPT Plus or Pro subscription, or any other provider on [models.dev](https://models.dev). den starts with a good model for it, which Enter keeps, and asks for its API key; a ChatGPT subscription signs in in the browser instead.

Then write to the bot. Run `den setup` again to finish a setup that stopped halfway; it skips what is done.

## How it works, and why it is safe

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
- **Outward actions wait for you.** Sending mail, inviting people, pushing to a branch the agent did not create, deleting things, and writes to your own APIs show up in Telegram with Allow and Deny. Nothing is sent before Allow, and a question nobody answers in 15 minutes expires. For a request you make often, such as a push to one branch or one kind of write to your API, **Always** allows that kind from then on; mail and invitations are asked about every time.
- **A log of what went out.** `/log` lists what the agent changed outside in the last day, and how: without a question, allowed by you, by your rule, denied, or expired. It also lists what you always allow, with a button to take each back.
- **Only your chat.** The bot answers the one Telegram chat it was set up with.

What den does not stop: inside its container the agent works with full access, it can reach the internet, and it reads what you give it, such as your mail and notes. A prompt hidden in a page could make it send that content somewhere. Give it the accounts and repositories it needs, and no more. The model provider sees your conversations.

## Chat

Each message to the bot becomes a turn of the agent's current session, and the reply comes back to the chat. While a turn runs, one message shows its latest steps in short, such as `Searched “lightpanda 1.0”` or `→ Running grep…`, and goes away when the reply arrives. Messages you send while the agent works go together as the next turn.

| Command | What it does |
|---|---|
| `/new` | Start a new session with the next message |
| `/sessions` | Continue a recent session, from the chat or from a job |
| `/model` | Choose the model: the last ones chosen are one tap away, **All models** lists every provider's models, and `/model deep flash` finds one by name. **Second model** chooses the one for jobs set to it |
| `/effort` | How hard the model thinks, such as `low` or `high`; higher is slower and costs more |
| `/again` | Ask the second model to answer your last message again in the same session, or `/again <text>` for one message; the reply starts with `↻` and its model |
| `/stop` | Stop the current turn |
| `/compact` | Shrink the session to a summary now, when it fills at least 10% of the model's window |
| `/jobs` | Run, remove, or move a job between the two models |
| `/status` | The model, how full the session is, memory, and the next jobs |
| `/log` | What the agent changed outside in the last day, and what you always allow |
| `/usage` | Tokens used today and over the last 7 and 30 days |

When you reply to a message, the agent sees which one you mean. Photos, voice messages, and files you send are saved in the agent's `inbox/`; a photo also goes to a model that takes images, and a voice message comes with its transcript when [speech-to-text](#voice-messages) is set up. Telegram lets a bot download files up to 20 MB, and the agent sends files up to 50 MB.

After you have been away for 50 minutes, den compacts a long session while the provider still has it cached, so the next turns start small and cost less. The first reply after it says so: `🗜 While you were away, den compacted this session: 120k → 9k tokens.`

A chat session is an ordinary fx session: `den fx` opens the agent in your terminal, and `den fx session resume` continues a session there.

## Jobs

Ask the agent in the chat, or add a job yourself:

```bash
den job add --schedule "in 30m" "Remind me to call the bank."
den job add --name morning --schedule "0 9 * * 1-5" "Send me today's top three items from notes/todo.md."
den job ls
den job log morning
den job run morning
den job slot morning second
den job rm morning
```

Schedules are `in 30m`, `2026-11-10 09:00`, `every 2h`, a cron expression, or `@daily`, in your time zone. A job runs in a fresh session and sends you its reply under a heading with its name and model, such as `⏰ morning · deepseek-v4.1-flash`, unless the reply is `SILENT` or the job was added with `--silent`. A failed run is always reported. The first start adds `reflect`, the nightly review, at 04:00.

A job runs on the chat's main model or on the second one. Jobs name only which of the two, so changing the second model in `/model` changes every job on it; without a second model, those jobs run on the main one.

With a second model set, it also stands in for the main one. When the main model's provider refuses or fails a turn or a job, such as when its quota runs out, rate limits it, or has an outage, den runs it once more on the second model, and the reply starts with `↻` and why.

## Models

The setup adds one account, and `/model` in Telegram picks any of its models. `den login <provider>` adds another or changes its key, and its models appear in `/model`. `den connect codex` signs in to a ChatGPT subscription in your browser for its Codex models; den keeps the tokens and refreshes them.

fx reaches every provider through den's proxy, which also measures each request. `den stats` shows how long each provider stayed silent before the first token, whether the answer stalled, how fast it wrote, and what failed, and the agent reads the same numbers when you ask it why it was slow today.

## Services and approvals

**GitHub.** Create a fine-grained token limited to the repositories the agent may use, with Contents, Issues, and Pull requests set to read and write, and run `den secret set github_token`. The agent clones and pushes with plain `git`, and calls GitHub's API with `den fetch`. Creating a branch and changing a branch the agent created go at once. Changing any other branch, such as `main`, deleting a branch, pushing a tag, writing to someone else's repository, changing who can access one, or creating a public one waits for your answer. A push you do not allow fails with `den: the user did not allow this push`. Ask the agent to back up its workspace, and it pushes `space/` to an empty private repository of yours every night.

**Your own APIs.** Any API that takes a token in a header:

```bash
den service add crm https://crm.example.com --about "Deals API: GET /api/deals, POST /api/deals; docs at https://crm.example.com/docs"
```

den asks for the token. The agent calls `den fetch --secret crm https://crm.example.com/api/deals`, and den adds `Authorization: Bearer <token>`, only for `crm.example.com`. `--header X-Api-Key` sends the token in another header. Reads go at once and writes wait for your answer; `--ask never` lets the agent write on its own, and `--ask always` asks about every request. The agent reads your `--about` text with `den service ls`. `den service rm crm` removes the service and its token.

**Gmail and Google Calendar.** den uses an OAuth client from your own Google Cloud project, so no one else holds access to your mail. In the project, enable the Gmail API and the Google Calendar API, add the scopes `gmail.modify` and `calendar.events` under Data Access, publish the app under Audience (in Testing, Google expires the access in 7 days), and create a client of type Desktop app. Then run `den connect google <the client JSON you downloaded>`. It opens Google's sign-in; Google warns that the app is unverified, and under Advanced you continue to it. If you sign in on another device, the last page fails to load: paste its address into the terminal. An app for personal use needs no verification from Google, and `den disconnect google` revokes the access.

den asks before mail goes out, before an event with other people is created or changed, before anyone gets a calendar notification, and before an event is deleted, and shows the recipients, subject, and text. Reading, labels, archiving, trash, drafts, and your own events need no answer.

**Any mailbox.** `den connect mail` takes an address and an app password instead of Google sign-in, and checks the login. den knows the servers of Gmail, iCloud, Fastmail, Yahoo, AOL, Zoho, and GMX, and asks for others. The agent uses IMAP and SMTP on den's local ports, and den logs in to your server with the password the agent never sees. Each message sent waits for your Allow. An app password opens the whole mailbox; Google sign-in grants narrower access and covers the calendar.

## Search

`den search` works without setup, through the free endpoints of Exa, Keenable, and Parallel. A key gives higher limits: `den secret set exa_key`, or `deepseek_key`, `tavily_key`, or `brave_key`, and den asks that service first. A service that hits a limit rests for an hour, or a day when it runs out of quota, and den moves to the next. No service gets anything that names you.

## Voice messages

To transcribe in the container, add a [whisper.cpp model](https://huggingface.co/ggerganov/whisper.cpp) and your language to `env` in `~/.config/den/config.json`, then run `den restart`:

```json
"env": {
  "TZ": "America/New_York",
  "DEN_WHISPER_MODEL": "small-q8_0",
  "DEN_WHISPER_LANGUAGE": "en"
}
```

den downloads the model once and starts `whisper-cli` for each voice message, so the model takes memory only while it works. On a five-second message on arm64:

| Model | Cores | Time | Peak memory |
|---|---|---|---|
| `large-v3-turbo-q8_0` (874 MB) | 4 | 8 s | 1.1 GB |
| `large-v3-turbo-q8_0` | 2 | 15 s | 1.1 GB |
| `small-q8_0` (264 MB) | 2 | 4 s | 0.5 GB |

`large-v3-turbo` handles speech that mixes languages better. `DEN_WHISPER_LANGUAGE=auto` detects the language but runs the model twice.

A service with OpenAI's transcription API works instead: for Groq, set `DEN_TRANSCRIBE_URL=https://api.groq.com/openai/v1/audio/transcriptions` and `DEN_TRANSCRIBE_MODEL=whisper-large-v3-turbo` in `env`, and run `den secret set transcribe_key`.

## Skills and memory

The agent's workspace, `space/`, is a git repository with its `AGENTS.md`, its notes, and the skills it writes under `.fx/skills/`. den adds built-in skills it can use but not change:

- `den`: jobs, messages, files, services, and past sessions.
- `reflect`: the nightly review.
- `web`: reading pages, with [Lightpanda](https://lightpanda.io) for pages that need JavaScript.
- `media`: details, subtitles, and transcripts of videos and audio with yt-dlp and whisper.
- `python`: scripts and data work with uv, which fetches Python on first use.
- `charts`: dark, minimal charts sized for a phone.
- `data`: records that change, such as job openings with a status, in SQLite.
- `social`: reading X and Reddit without an API key.
- `model-stats`: how the model providers did, by hour.
- `github`, `google`, `mail`: the services above.

What the agent learns about a built-in skill goes to `notes/skills/<name>.md`, which the skill tells it to read.

## Reference

### Commands on your computer

| Command | What it does |
|---|---|
| `den setup` | Sets den up, or finishes a setup |
| `den update` | Updates the `den` command and its image from the latest release, and recreates the container; notes, sessions, and secrets stay |
| `den start`, `den stop`, `den restart` | The container |
| `den status`, `den logs [-f]` | The container and the secrets den has; den's log |
| `den login <provider>` | Adds a model provider or changes its key |
| `den connect google`, `mail`, `codex` | Gmail and Calendar, a mailbox, a ChatGPT subscription |
| `den service add`, `ls`, `rm` | Your own APIs |
| `den secret set <name>`, `ls`, `rm` | Any other secret, such as `github_token`; asks for its value |
| `den stats [--since 2h] [--json]` | How the model providers did |
| `den fx`, `den shell` | fx or a shell as the agent |

Any other command, such as `den job ls`, runs inside the container. The settings are in `~/.config/den/config.json`: the container's memory and CPUs, its environment, the folder for the agent's files, and the tool that runs it (`runtime`: `container`, `docker`, or `podman`). `den restart` applies them. Docker and Podman start den again after the computer restarts; with Apple's `container`, run `den start`.

The agent's files live in that folder on your computer. den's state and secrets live in the volume `den`, which keeps file permissions and survives `den update`. A shared folder may not keep permissions inside the container, so never put secrets in it.

### Inside the container

```
/usr/local/bin/{den,fx,lightpanda,whisper-cli}
/usr/share/den/skills/   built-in skills, read-only
/var/lib/den/            den's state: jobs, runs, chat, model calls   (volume)
  secrets/               root only
/run/den/den.sock        the socket the agent's den command talks to
/home/agent/             the agent's home                             (your folder)
  .fx/                   fx settings and sessions
  .agents/skills/        links to the built-in skills
  .local/bin/            tools the agent installs, such as yt-dlp
  space/                 the agent's workspace, a git repository
    AGENTS.md            persona, rules, and memory
    .fx/skills/          the skills the agent writes
    notes/               lasting results, one file per topic
  archive/               past sessions as markdown
  inbox/                 photos and files from the chat
```

### Environment

Set these in `env` in `~/.config/den/config.json`.

| Variable | Default |
|---|---|
| `TZ` | your computer's time zone |
| `DEN_WHISPER_MODEL` | none: no local transcription |
| `DEN_WHISPER_LANGUAGE` | `auto` |
| `DEN_TRANSCRIBE_URL` | none: no transcription service |
| `DEN_TRANSCRIBE_MODEL` | none: the service picks |
| `DEN_JOB_TIMEOUT` | `30m`, for a job or a chat turn |

### Building it yourself

In a checkout, `go build -o ~/.local/bin/den ./cmd/den` builds `den`, and `den setup` run there builds the image from the checkout instead of pulling it; `den update` then rebuilds it, and the `den` command too when Go is installed. `scripts/release.sh` builds `den` for every system `install.sh` supports.

The image takes fx from a release of [a fork](https://github.com/unsady/fx) that carries the few changes den needs, and checks its digest. To try your own fx, build it in an fx checkout with `zig build -Dtarget=aarch64-linux -Doptimize=ReleaseSafe` and copy `zig-out/bin/fx` to `build/fx` before `den update`.

## License

Apache-2.0. See [LICENSE](LICENSE).
