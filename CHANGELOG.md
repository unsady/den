# Changelog

What changes for people who use den. Each change lands under Unreleased;
a release renames that section to its version, and its text becomes the
release notes.

## Unreleased

### New

- `den map` asks a cheap model the same question about each line of a JSONL file, one short request per item, and writes one JSON answer per item with a count of each answer, the tokens, and the cost. It says how far it is every 15 seconds, and marks the instructions for Claude models so the provider caches them. The new `map` skill tells the agent to use it for many similar items, such as job ads or emails, instead of reading them all in its own session: on 100 job ads it took 4 minutes and 0.16M tokens where the agent took 17 minutes and 11.7M.

## v0.2.0

To update from v0.1.1, run the install line once, since its `den update` updates only the image:

```sh
curl -fsSL https://raw.githubusercontent.com/unsady/den/main/install.sh | sh
den update
```

From now on, `den update` updates the `den` command too.

### New

- `den update` also updates the `den` command itself from the latest release, after checking its checksum.
- Setup and `den login` take an Anthropic, xAI, or Vercel AI Gateway API key, through each one's OpenAI-compatible API. Setup's list of other providers starts with the popular ones, such as OpenRouter, DeepSeek, OpenCode, Z.AI, and Moonshot AI.
- `/compact` shrinks the chat's session to a summary now and says how many tokens it went from and to. fx compacts a session from 10% of the model's window, so a smaller one is left as it is.
- `/sessions` lists the sessions you used last first and keeps job runs in a list of their own behind **Job runs**, so hourly jobs no longer push your chats out. `/sessions <words>` finds a session by its title.
- `/bug` gives the link to report a bug or suggest an idea on GitHub, and den@bazutya.com for those who would rather write.
- `sqlite3` comes with den, and the new `data` skill tells the agent to keep records that change, such as job openings with a status, in a SQLite database under `~/space/.data/`, which a new space keeps out of git.
- The agent knows the names of the APIs you added with `den service add` from the start of each session, so it reaches for them without being told.

### Better

- A ``` block or `inline code` in the agent's messages shows in Telegram as code that copies with a tap, and a Markdown link such as [Open VisuAlgo](https://visualgo.net/en) shows as a link with that text.
- The message that shows the agent's steps arrives without a sound, so only the reply makes your phone ring.
- Once answered or expired, a question about a request, a push, or a mail shrinks to one line, such as “news: POST /api/events” and ✓ Allowed.
- Progress steps name the program a command runs, even inside `$(…)`, loops, `if`, and multi-line scripts, and name den commands, such as “Running den stats”. A command still running in the background reads “Waiting for jq”.
- `/status` sums jobs up in one line with the next one to run, and names only jobs whose last run failed. Choosing the second model answers with ⚙︎, like the main model.
- den keeps its instructions for every session in fx's own `~/.fx/AGENTS.md`, which it rewrites on each start, instead of adding them to your first message. Your space's `AGENTS.md` stays yours.
- The agent is asked to do the work itself and start a subagent only for a long, independent part, since subagents spend the same model's limits.
- The web skill tells the agent to read every page with `web_fetch`, which turns HTML into clean text, and to keep `curl` for APIs, downloads, and requests with headers.
- den comes with fx synced with upstream fx on 2026-11-11.

### Fixed

- When a subagent reports after the agent has answered, the chat and jobs get the agent's answer instead of a bare “SILENT” or “Done.”, which kept some job replies from being sent.
- With a ChatGPT subscription, `/status` shows the context size and the model's window, and den compacts a long session while you are away: den read the wrong token count and missed the window before.
- When Google is connected, the agent sends Gmail through it instead of asking you to run `den connect mail`.

## v0.1.1

- `den setup` asks which account pays for the model: OpenCode Go, OpenRouter, DeepSeek, a ChatGPT Plus or Pro subscription, or any other provider on models.dev. A model is ready for each, and lists filter as you type.
- Setup's questions take arrow keys and masked input, and each finished step shows ✓ or ✗. A wrong bot token asks again instead of ending the setup.
- Setup checks that den's container reaches the internet and points at a VPN when it does not.
- `install.sh` works on macOS again: its `sh` stopped at the download message in a UTF-8 locale.
- An Always rule now covers every item of a path with long ids such as ULIDs, not only the one approved.
- A ChatGPT subscription connected first becomes den's model.

## v0.1.0

- First public release.
