# Reference

`install.sh` puts `den` in `~/.local/bin`, checks its digest, and runs `den setup`. The setup gets den's image, starts the container, and asks for the Telegram bot and the model's account; for a provider it starts with a good model, which Enter keeps.

## Commands on your computer

| Command | What it does |
|---|---|
| `den setup` | Sets den up, or finishes a setup |
| `den update` | Updates the `den` command and its image from the latest release, and recreates the container; notes, sessions, and secrets stay |
| `den start`, `den stop`, `den restart` | The container |
| `den status`, `den logs [-f]` | The container and the secrets den has; den's log |
| `den login <provider>` | Adds a model provider or changes its key |
| `den connect`, `den connect google`, `mail`, `codex`, `grok`, `claude` | Gmail, Calendar, and Drive, a mailbox, a ChatGPT, Grok or Claude subscription; alone, a list to pick from |
| `den service add`, `ls`, `rm` | Your own APIs; `add` alone asks for each detail |
| `den secret set <name>`, `ls`, `rm` | Any other secret, such as `github_token`; asks for its value |
| `den backup setup`, `den backup`, `ls`, `restore <file>` | Encrypted backups of den's state to a folder and S3: [Backups](backup.md) |
| `den stats [--since 2h] [--json]` | How the model providers did |
| `den fx`, `den shell` | fx or a shell as the agent |

Any other command, such as `den job ls`, runs inside the container. `den sessions ls` lists the latest sessions, and `den sessions rm <id>` deletes one and makes the chat forget it. The settings are in `~/.config/den/config.json`: the container's memory and CPUs, its environment, the folder for the agent's files, the folder for backups (`backups`), and the tool that runs it (`runtime`: `container`, `docker`, or `podman`). `den restart` applies them. Docker and Podman start den again after the computer restarts; with Apple's `container`, run `den start`.

The agent's files live in that folder on your computer. den's state and secrets live in the volume `den`, which keeps file permissions and survives `den update`. A shared folder may not keep permissions inside the container, so never put secrets in it.

## Inside the container

```
/usr/local/bin/{den,fx,lightpanda,whisper-cli}
/usr/share/den/skills/   built-in skills, read-only
/var/lib/den/            den's state: jobs, runs, chat, model calls   (volume)
  secrets/               root only
/var/backups/den/        backups, encrypted                           (your folder, once set up)
/run/den/den.sock        the socket the agent's den command talks to
/home/agent/             the agent's home                             (your folder)
  .fx/                   fx settings and sessions
  .agents/skills/        links to the built-in skills
  .local/bin/            tools the agent installs, such as yt-dlp
  space/                 the agent's workspace, a git repository
    AGENTS.md            memory: the user and the environment
    .fx/skills/          the skills the agent writes
    notes/               lasting results, one file per topic
  archive/               past sessions as markdown
  inbox/                 photos and files from the chat
```

Do not run git in the agent's home on your computer: [Security](security.md#the-agents-folder) says why.

## Environment

Set these in `env` in `~/.config/den/config.json`.

| Variable | Default |
|---|---|
| `TZ` | your computer's time zone |
| `DEN_WHISPER_MODEL` | none: no local transcription |
| `DEN_WHISPER_LANGUAGE` | `auto` |
| `DEN_TRANSCRIBE_URL` | none: no transcription service |
| `DEN_TRANSCRIBE_MODEL` | none: the service picks |
| `DEN_JOB_TIMEOUT` | `30m`, for a job or a chat turn |
| `DEN_JOBS` | `2`: how many jobs run at once; the rest wait |
| `DEN_CLAUDE_PROCESSES` | `2`: how many Claude processes (about 250 MB each) den keeps; it closes the least recently used idle one beyond that |
