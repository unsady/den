# Chat, jobs, and models

## Chat

Each message to the bot becomes a turn of the agent's current session, and the reply comes back to the chat. While a turn runs, one message shows its latest steps in short, such as `Searched “lightpanda 1.0”` or `→ Running grep…`, and goes away when the reply arrives. A message you send while the agent works joins that turn, marked ✍: the agent reads it after the step it is on, such as a command that is running, and can change course without starting over. A photo, or a reply to a job's message, waits and goes as the next turn, marked 👀.

| Command | What it does |
|---|---|
| `/new` | Start a new session with the next message |
| `/sessions` | Continue a recent session: the ones you used last come first, **Job runs** lists the jobs' sessions, and `/sessions taxes` finds one by title |
| `/model` | Choose the model: the last ones chosen are one tap away, **All models** lists every provider's models, and `/model deep flash` finds one by name. **Second model** chooses the one for jobs set to it |
| `/effort` | How hard the model thinks, such as `low` or `high`; higher is slower and costs more |
| `/bg` | Run a task in the background, in a session of its own: `/bg <task>`. Its answer comes under the task's name, and a reply to it goes on in that session |
| `/stop` | Stop the current turn |
| `/compact` | Shrink the session to a summary now, when it fills at least 10% of the model's window |
| `/jobs` | List the jobs; ask the agent to change, run, or remove one |
| `/status` | The model, how full the session is, memory, and the next jobs |
| `/log` | What the agent changed outside in the last day, and what you always allow |
| `/usage` | Tokens used today and over the last 7 and 30 days |
| `/bug` | Where to report a bug or suggest an idea: GitHub issues or den@bazutya.com |
| `/help` | What the bot does |

When you reply to a message, the agent sees which one you mean. Photos, voice messages, and files you send are saved in the agent's `inbox/`; a photo also goes to a model that takes images, and a voice message comes with its transcript when [speech-to-text](voice.md) is set up. Telegram lets a bot download files up to 20 MB, and the agent sends files up to 50 MB.

After you have been away for 50 minutes, den compacts a long session while the provider still has it cached, so the next turns start small and cost less. The first reply after it says so: `🗜 While you were away, den compacted this session: 120k → 9k tokens.`

A chat session is an ordinary fx session: `den fx` opens the agent in your terminal, and `den fx session resume` continues a session there.

## App

The den app is another way into the same chat: the same session, models, and questions as Telegram. A message you write in the app is answered there; one you write in Telegram is answered in both, and job results and questions show in both. Answer a question in one, and it shrinks in the other.

```bash
den app pair
```

`den app pair` publishes den's port, 4400, on this machine's Tailscale address only, so the app reaches den from anywhere in your tailnet and from nowhere else; `--address <ip>` picks another address of the machine instead. It prints a `den://` link with a new token to paste in the app's settings. den keeps only the token's hash, among its secrets, so the agent cannot read it, and every request without it is refused. Each link is for one device: run `den app pair` again for a phone and a laptop, and both stay in; `den app unpair` shuts every app out and closes the port.

Tailscale encrypts the connection. On an address outside it, such as your home network, the token and the chat travel unencrypted, so use one only on a network you trust.

## Jobs

Ask the agent in the chat, or add a job yourself:

```bash
den job add --name call-the-bank --title "Call the bank" --schedule "in 30m" "Remind me to call the bank."
den job add --name morning-brief --title "Morning brief" --schedule "0 9 * * 1-5" "Send me today's top three items from notes/todo.md."
den job ls
den job log morning-brief
den job run morning-brief
den job edit morning-brief --title "Morning digest" --schedule "0 8 * * 1-5"
den job slot morning-brief second
den job rm morning-brief
```

Schedules are `in 30m`, `2026-11-10 09:00`, `every 2h`, a cron expression, or `@daily`, in your time zone. A job runs in a fresh session and sends you its reply under a heading with its name and model, such as `⏰ morning · deepseek-v4.1-flash`, unless the run ends with `den quiet` or the job was added with `--silent`. A failed run is always reported. den adds two jobs of its own: `reflect`, the nightly review, at 04:00, and `tidy` at 05:00, which keeps the notes and skills easy to search. A job you remove stays removed.

A job runs on the chat's main model or on the second one. Jobs name only which of the two, so changing the second model in `/model` changes every job on it; without a second model, those jobs run on the main one.

## Models

The setup adds one account, and `/model` in Telegram picks any of its models. `den login <provider>` adds another or changes its key, and its models appear in `/model`. `den connect codex` signs in to a ChatGPT subscription in your browser for its Codex models; `den connect grok` does the same for a Grok subscription. den keeps the tokens and refreshes them; the agent gets only a stand-in. `den connect claude` takes the token that `claude setup-token` from Claude Code prints, for a Claude Pro or Max subscription: den runs Anthropic's own `claude` command as the model and keeps the token from the agent. Each conversation runs a `claude` process of about 250 MB, so give den's container 2G when you use it.

fx reaches every provider through den's proxy, which also measures each request. `den stats` shows how long each provider stayed silent before the first token, whether the answer stalled, how fast it wrote, and what failed, and the agent reads the same numbers when you ask it why it was slow today.

## Skills and memory

The agent's workspace, `space/`, is a git repository with its `AGENTS.md`, its notes, and the skills it writes under `.fx/skills/`. den adds built-in skills it can use but not change:

- `den`: jobs, messages, files, services, and past sessions.
- `reflect`: the nightly review.
- `tidy`: folding logs and reports into topic notes, and keeping skills short.
- `web`: reading pages, with [Lightpanda](https://lightpanda.io) for pages that need JavaScript.
- `media`: details, subtitles, and transcripts of videos and audio with yt-dlp and whisper.
- `python`: scripts and data work with uv, which fetches Python on first use.
- `charts`: dark, minimal charts sized for a phone.
- `data`: records that change, such as job openings with a status, in SQLite.
- `map`: classifying or extracting from many similar items with `den map`, which asks a cheap model about each item in its own short request.
- `social`: reading X and Reddit without an API key.
- `model-stats`: how the model providers did, by hour.
- `github`, `google`, `mail`: the services above.

What the agent learns about a built-in skill goes to `notes/skills/<name>.md`, which the skill tells it to read.
