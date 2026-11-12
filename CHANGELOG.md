# Changelog

What changes for people who use den. Each change lands under Unreleased;
a release renames that section to its version, and its text becomes the
release notes.

## Unreleased

### New

- `den map` asks a cheap model the same question about each line of a JSONL file, one short request per item, and writes one JSON answer per item with a count of each answer, the tokens, and the cost. It says how far it is every 10 seconds, and marks the instructions for Claude models so the provider caches them. Each run needs your Allow in Telegram, asked with a short question, with Always to allow a model for good. A run of the chat's agent goes on in den's background, so you can keep talking to the agent: one message lists the runs going on and how far each is, and goes away when they end, and the agent hears what came of each in a new turn; it runs 4 items at once, at most 8, and `--effort` sets the model's reasoning effort, such as `none` for speed over care. A ChatGPT subscription works too when the model is named, such as `--model codex/gpt-6-luna`, and Codex reuses its cache of instructions longer than about 1,000 tokens. The new `map` skill tells the agent to use it for many similar items, such as job ads or emails, instead of reading them all in its own session: on 100 job ads it took 4 minutes and 0.16M tokens where the agent took 17 minutes and 11.7M.
- `/bg <task>` runs a task in a new session of its own while you keep talking in the chat. A model names it in two or three words, such as ⚙ compare-models, the second model when you chose one, and its answer comes headed with that name, and a reply to it goes on in that session. `/stop` stops background tasks too.
- Reply to a job's message, or to a message from an earlier session, to go on in that session: den runs the turn there, heads the answer with the job's name or the session's title, and the chat stays in its own session. den tells the messages apart by their Telegram ids, not their text, for the last 500 it sent. A turn the chat left with `/new` while it ran now answers headed with its session's title too.

### Better

- The agent no longer waits on a question in Telegram. Mail, pushes to branches the agent did not create, writes through `den fetch` that need your Allow, and `den map` runs return at once; den keeps the action, takes it once you allow it, and tells the agent what came of it in a new turn, so it can carry on, such as reporting the result. Before, the action failed when the agent's turn ended first, which some models did within a second. A question now waits up to a day instead of 15 minutes. A kept push that GitHub refuses because its branch moved meanwhile is reported, and the agent pushes again.
- A question about a write to your own API or a GitHub repository has a **1 hour** button: it allows this request and every other write to the same API or repository, but deletes, for the next hour, so a task with many writes takes one tap.
- `den service add --ask deletes` asks only before a delete, and lets other writes through.

### Fixed

- A job's runs past the latest 50 showed up in `/sessions` as chat sessions, and every run's fx session stayed on disk for good. den now remembers every job run's session, and deletes it together with the run's record unless you went on in it from the chat.
- What came of an action you allowed could get lost after `/stop`, or reach a new session's agent after `/new`. den now sends it to you instead.
- The agent could get den's secrets or forge your approvals through links in its own fx profile: den read provider keys and settings there as root and followed the links. den now reads and writes the agent's profile as the agent.
- A push that GitHub refused could still make a branch the agent's own, such as `main`, so later pushes to it went without a question. A branch is now the agent's only once GitHub took the push that created it, and den refuses a push with a line it cannot read.
- A question too long for one Telegram message was cut, which could hide part of what you allowed; den now refuses to ask it. A long body in a question says how much of it is not shown, and a button left from before a restart can no longer answer a new question.
- More GitHub writes now wait for you: den lets through only issues, comments, labels, milestones, pull requests short of merging, and git objects in your own repositories. Changing files through the API, merging, tags, releases, workflow runs and repository transfers used to go without a question. Any GraphQL mutation now asks too, also when it hides behind a query in the same request.
- Some Google requests got past the questions: batch requests and method-override headers are now refused, and the calendar asks before an event with guests changes, before an event is cancelled, and before guests are notified any way. A draft you allowed is sent only if it is still what you saw.
- **Always** covers less: never another repository with a similar name, nor a request with a query string or a GraphQL body. In a path, only numbers, UUIDs, and ULIDs stand for any one of their kind.
- The model proxy passed any path of a provider's host on with its key, and any path of ChatGPT with your token. Keys now reach only their provider's API, and the ChatGPT token only Codex's models.
- The question about an email now shows `Reply-To` and `Sender`, and the HTML version when it says something other than the plain text, since most mail apps show the HTML.
- The agent could delete mail for good over IMAP. den now answers `NO` to expunging, deleting folders, and marking messages deleted; the agent moves mail to the trash.
- den commands run from your computer, other than those that handle secrets, now run as the agent, not as root.
- A service's answer or a command's output in den's messages to the agent is now quoted apart, so it cannot pass for den's own words.
- The README now warns that a branch the agent pushes runs your GitHub Actions, and that the agent's home folder is not a place to run git on your computer.

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
