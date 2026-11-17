# Changelog

What changes for people who use den. Each change lands under Unreleased;
a release renames that section to its version, and its text becomes the
release notes.

## Unreleased

## v0.9.0

### New

- The den app: `den app pair` lets it into the chat over your Tailscale network with a token of its own. It shares the session, the models, job results, and questions with Telegram. It shows a session as you saw it, from a journal den keeps from now on; earlier sessions open empty.
- A message you send while the agent works joins the turn, marked ✍, instead of waiting for it to end: the agent reads it after the step it is on, so "stop, not that file" or "check X too" changes what it does now. A photo, or a reply to a job's message, still waits for the next turn.

### Better

- A new chat session gets a short title in /sessions and the app instead of its first message, made from your message and the reply, so a photo or a link gets a title too: den asks Claude's haiku through the claude command, or the chat's own model with other providers. Codex and Grok sessions keep the title fx gives them.
- A question you answer shrinks to its answer wherever it shows, and so does one that expires.
- A push that waits for your answer names its commits: the question lists each branch with its count of commits and files, their titles, and a link to the comparison on GitHub, and says when the push rewrites history. Allow moves the branch to the commits you saw; if the branch moved meanwhile, den leaves it alone and tells the agent.
- A request too long to show in its question comes with its whole body as a file, sent before the buttons.
- `/log` shows the repeating jobs the agent added or removed.
- The 1 hour button no longer covers changes to access, secrets, workflows, or branch rules; those ask every time.
- Messages that wait for the agent to finish a turn survive a restart of den.
- If den stops before you answer a question, the agent learns that the action did not happen, instead of waiting.

### Removed

- `/again`, and the second model standing in when the main model's provider fails. The second model now only runs the jobs set to it.

### Fixed

- `den fetch` refuses `.` and `..` in a path, which could reach a path the rule did not name.
- The release is published only once its image is on ghcr.io, so an update never finds a release without its image.

### Security

- Under Docker and Podman, the container runs with `no-new-privileges`, so nothing the agent runs gains more rights through a setuid program, and with at most 512 processes, so a command that forks without end stops in the container.

## v0.8.1

### Better

- `/sessions` lists each session once, as a button with its title and age, and • marks the current one.
- `/jobs` has no ▶ Run button under each job; ask the agent to run one.
- `/sessions` has no 🗑 button beside each session, so the sessions get the whole width. `den sessions rm <id>` still deletes one.
- Questions and messages in the chat no longer repeat what they already show. Once you answer a question, it says what Always or 1 hour allowed, such as mail to one address or every write to a service.

## v0.8.0

### New

- A nightly `tidy` job, at 05:00, folds logs and reports from the agent's notes into their topics, stops the jobs that write them, and keeps skills short. The nightly review now only learns from your conversations. An update adds the job; if you remove it, it stays removed.
- Each session starts with a list of the agent's notes and what each holds, from a one-line description at the top of each note, so the agent opens the right one.
- A recurring job's prompt names the skill to use, and the rules live in the skill, where the nightly jobs can fix them.
- A `/bg` task is named in English, in two words, three at most, whatever the task's language, so the names of tasks and jobs read alike. ChatGPT and Grok subscriptions name tasks too, instead of falling back to the task's first words.
- The agent sets size limits in words, which models keep to with room to spare and which count the same in any language: memory sections, skills, and its own rules.
- A mail to one person can be allowed for good or for an hour: the question has Always and 1 hour buttons for that address, over SMTP and when the agent sends through Google (a draft still asks every time). A mail to several people asks each time.
- A reply to a message of a job goes to the model that ran the job, and the files of an album reach the agent as one message.

### Fixed

- The agent sends mail with Google as plain UTF-8 text, not as base64 that the recipient read as it was.
- Shortening a skill no longer drops limits its rules set, such as how many records a run reviews.

## v0.7.0

### New

- With a Claude subscription the agent sees images: the photos you send, and the images it opens itself.

### Fixed

- When a turn fails for a reason den knows, such as an image sent to a model that cannot see images, the chat says what to do instead of "exit status 1".
- **Bold** text in the agent's messages shows bold in Telegram instead of with its asterisks.

## v0.6.0

### New

- The agent's memory has a Soul section for who the agent is, such as its name, gender, and manner, apart from User, which is about you. A new space has it; in an existing one the agent adds it when you tell it who it is.
- `den stats` also shows how Claude models did: the first token, stalls, and writing speed.

### Better

- den comes with fx 0.0.17, upstream fx's latest release, and from now on takes fx only from upstream's releases. Requests keep retrying through network drops, gateway errors, and rate limits; more of each conversation comes from the prompt cache; and long sessions stay responsive.
- A step that opens a skill reads “Using media”, and one that searches fx's toolbox reads “Looking for a tool”.

## v0.5.0

### New

- `den connect grok` connects a Grok subscription, such as SuperGrok, in your browser, and its models appear in `/model` with their effort levels. den keeps the tokens and refreshes them; the agent gets only a stand-in, as with ChatGPT.

- `den connect claude` connects a Claude Pro or Max subscription with the token `claude setup-token` prints. den runs Anthropic's own `claude` command as the model, with fx's tools, and keeps the token from the agent; `/model` offers opus, sonnet and haiku with their effort levels. Setup offers it too, and `den map --model claude/haiku` runs on it. Each conversation runs a `claude` process of about 250 MB: give den's container 2G (`"memory": "2G"`) to use it for chats and jobs at once. `DEN_CLAUDE_PROCESSES` sets how many idle ones den keeps.

- `den sessions rm <id>` deletes a session, and the chat forgets it: if it was the chat's own session, the next message starts a new one. `den sessions ls` lists the latest sessions with their ids.

- `/sessions` has a 🗑 button beside each session, which asks before it deletes the session.

### Better

- At most two jobs run at once, and the rest wait their turn. Routines set for the same morning used to start together and could take more memory than den's container has. `DEN_JOBS` changes the number.

- Picker buttons stand in rows of at most three, so Telegram no longer shortens them on a phone, and the effort shows as just its level: "haiku · claude · medium".

- The agent writes a rule you give it as one plain line in your words, with no exception left to its own judgment, and replaces the line when you correct it. Weaker models used to add loopholes that let a later chat skip the rule.

- When you ask for shorter replies, the agent saves the limit as a number, such as "at most 40 words, no lists, no links", and follows it over its own defaults. Weaker models used to keep to "1-2 sentences" by packing a paragraph into each one.

### Fixed

- A reply that ends with SILENT on its own line sends nothing. A job used to send its status with the word SILENT under it.

## v0.4.1

### Better

- Notes hold one file per topic, never reports of runs. The nightly reflection folds such reports into their topic, stops jobs that write one on every run, and shortens skills over about 4,000 characters.
- den's rules for the agent all live in the file den rewrites on each start, so every install gets new ones. A new space's `AGENTS.md` starts as just the memory sections, and an existing one keeps its old copy of the rules until you delete them.
- Lightpanda 1.0.4. The image now checks the browser download against its checksum.

### Fixed

- A message that needs no answer, such as "ok", gets none. The chat used to send the word SILENT.
- `den update` removes the image it replaces. Each update used to leave the old image on disk, about 1.4 GB each with Apple's container. The fix is in the `den` command itself, so run the install script again to get it.

## v0.4.0

### Better

- The agent answers like an assistant, not a coding tool: den gives fx a base prompt of its own, so a reply is the result, such as the jobs it found or that there are none, without how it got there, what is done and what is not, Markdown tables, or headings.
- You hear from the agent once its work is done. After a `den map` run it started, or den's word that one ended, the agent goes on without a message about each step, and the agent gets only the run's summary instead of every progress line.
- A screening the agent repeats, such as new job ads every day, stays one quick `den map` pass: the `map` skill now has a recipe for it, with the criteria in one prompt file and a reply that lists only the matches. When a result is wrong, the agent fixes the rule instead of adding review steps, and the nightly review shortens skills that grow past about 4,000 characters.
- When the main model's provider fails a chat turn, den no longer answers with the second model by itself: it says what failed, with a button that asks the second model. Jobs still move to the second model on their own.
- `/jobs` is a plain list of the jobs, when each runs next, and how its last run went, with a button to run each now. Its Remove and model buttons, which did not say which job they were for, are gone: ask the agent to change, move, or remove a job.
- Job replies no longer wake you: the nightly review always arrives without a sound, and so does any other repeating job's reply between 23:00 and 8:00 your time. A one-time reminder still rings.

### Fixed

- A turn on some models, such as Qwen on OpenCode Go, failed with `InvalidChunk` as soon as the model called a tool. It works now.

## v0.3.0

### New

- `den map` asks a cheap model the same question about each line of a JSONL file, one short request per item, and writes one JSON answer per item with a count of each answer, the tokens, and the cost. It says how far it is every 10 seconds, and marks the instructions for Claude models so the provider caches them. Each run needs your Allow in Telegram, asked with a short question, with Always to allow a model for good. A run of the chat's agent goes on in den's background, so you can keep talking to the agent: one message lists the runs going on and how far each is, and goes away when they end, and the agent hears what came of each in a new turn; it runs 4 items at once, at most 8, and `--effort` sets the model's reasoning effort, such as `none` for speed over care. A ChatGPT subscription works too when the model is named, such as `--model codex/gpt-6-luna`, and Codex reuses its cache of instructions longer than about 1,000 tokens. The new `map` skill tells the agent to use it for many similar items, such as job ads or emails, instead of reading them all in its own session: on 100 job ads it took 4 minutes and 0.16M tokens where the agent took 17 minutes and 11.7M.
- `/bg <task>` runs a task in a new session of its own while you keep talking in the chat. A model names it in two or three words, such as ⚙ compare-models, the second model when you chose one, and its answer comes headed with that name, and a reply to it goes on in that session. `/stop` stops background tasks too.
- Reply to a job's message, or to a message from an earlier session, to go on in that session: den runs the turn there, heads the answer with the job's name or the session's title, and the chat stays in its own session. den tells the messages apart by their Telegram ids, not their text, for the last 500 it sent. A turn the chat left with `/new` while it ran now answers headed with its session's title too.

### Better

- The agent no longer waits on a question in Telegram. Mail, pushes to branches the agent did not create, writes through `den fetch` that need your Allow, and `den map` runs return at once; den keeps the action, takes it once you allow it, and tells the agent what came of it in a new turn, so it can carry on, such as reporting the result. Before, the action failed when the agent's turn ended first, which some models did within a second. A question now waits up to a day instead of 15 minutes. A kept push that GitHub refuses because its branch moved meanwhile is reported, and the agent pushes again.
- A question about a write to your own API or a GitHub repository has a **1 hour** button: it allows this request and every other write to the same API or repository, but deletes, for the next hour, so a task with many writes takes one tap.
- `den service add --ask deletes` asks only before a delete, and lets other writes through.
- `/compact` shows den typing while it works.

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
