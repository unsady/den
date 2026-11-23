# den

A personal AI agent you talk to in Telegram. It runs on your own computer, in a container, and does real work: reads and answers mail, keeps your calendar, edits your Google Docs and Sheets, runs jobs on a schedule, pushes to GitHub, calls your own APIs, searches the web, and remembers what it learns.

den is built on [fx](https://github.com/vercel-labs/fx), a small and fast agent CLI. Idle, it is one small process waiting for your messages, and the agent never sees a password, token, or key.

## Install

You need a Telegram account, an account with a model provider or an AI subscription, and Apple's [`container`](https://github.com/apple/container) or Docker on a Mac, Docker or Podman on Linux.

```bash
curl -fsSL https://raw.githubusercontent.com/unsady/den/main/install.sh | sh
```

The setup asks for two things:

- A Telegram bot's token, from [@BotFather](https://t.me/BotFather). You then send the bot any message, and it answers only your chat from then on.
- The account that pays for the model: OpenRouter, DeepSeek, OpenCode Go, a ChatGPT, Grok, or Claude subscription, or any provider on [models.dev](https://models.dev).

Run `den setup` again to finish a setup that stopped halfway.

## Try it

Write to your bot:

- Every weekday at 9, send me the weather for the day.
- Remind me in 30 minutes to call the bank.
- Find what happened in AI this week and sum it up in five lines.
- What can you do, and what can I connect?

Send photos, files, and voice messages too. `/help` lists the bot's commands.

## Connect more

The agent knows how: ask it "help me connect my mail" or "connect Todoist", and it answers with the command to run on your computer. You enter the secret there, never in the chat.

| What | Command |
|---|---|
| Any mailbox | `den connect mail` |
| Gmail, Calendar, and Drive | `den connect google` |
| GitHub | `den secret set github_token` |
| Your own API | `den service add` |
| Voice messages | [Voice](docs/voice.md) |

[Services](docs/services.md) has the details.

## Safe with your accounts

- den holds every secret and adds it to the agent's requests itself, only for the host it belongs to.
- Sending mail, pushing to `main`, deleting things, and writes to your APIs wait for your Allow in Telegram.
- The bot answers only your chat.

Inside its container the agent works with full access and reaches the internet, so give it only the accounts it needs. [Security](docs/security.md) says how it works and what it does not stop.

## More

- [Chat, jobs, and models](docs/chat.md): the bot's commands, the app, jobs, models, memory
- [Services](docs/services.md): your own APIs, GitHub, Google, mail, search
- [Voice](docs/voice.md): transcribing voice messages
- [Backups](docs/backup.md): den's state, encrypted, to a folder and S3
- [Security](docs/security.md): how den keeps secrets and what waits for you
- [Reference](docs/reference.md): commands, settings, the container's layout
- [Development](docs/development.md): building den yourself

Report a bug or suggest an idea in [issues](https://github.com/unsady/den/issues) or at den@bazutya.com.

## License

Apache-2.0. See [LICENSE](LICENSE).
