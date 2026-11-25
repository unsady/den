# Changelog

What changes for people who use den. Each change lands under Unreleased;
a release renames that section to its version, and its text becomes the
release notes.

## Unreleased

### Added

- Telegram shows a long reply as it is written, as a draft that the reply takes the place of, like the app does.
- `den config` shows den's settings, and `den config set` changes the container's memory or cores, or a variable of den's environment such as `DEN_WHISPER_MODEL`, and restarts den: no more editing `config.json` by hand.
- Push notifications for the phone app: a reply, a job's result, a notice, or a question reaches the phone while the app is closed, once den has an Apple push key (`apns.p8` and `apns.json` among its secrets).
- `den send --source` links a file to the page it came from, such as a shop's page of a product: Telegram adds the site's name as a link to the caption, and the apps show a link icon by its caption.
- When a turn or a job changes the agent's memory, the app marks the reply it sends with "Memory updated", and shows the agent's short lines on what changed, which it leaves with the new `den remembered`.
- While the agent works, the app's line of its steps says Thinking… while the model thinks between steps, and why it waits when the model's provider does not answer, so a long pause no longer looks like a hang.
- The apps show how full a session's context is and can compact any session, not only Telegram's; the feed marks where den compacted a session.

### Fixed

- A chat turn whose model does not answer for 3 minutes fails with a button to retry, instead of waiting for the turn's 30 minutes to run out.
- Deleting a session in the app while the agent works in it no longer fails now and then with "send /stop first".
- `den app unpair` also stops the pushes to the phones that were paired.
- Backups to a folder den cannot give to its owner still drop the old snapshots, so the folder no longer fills up, and den no longer reports a failed backup when its own restart cut one short.
- Claude conversations no longer pile up on disk: den deletes Claude's copy of one once it can no longer resume it, and all of them when it starts.

## v0.16.0

### Added

- Search in the app: find a message in any session, archived ones too, by the words it holds.
- Quick capture: share text, a link, or a picture to den from the iPhone's share sheet, or press Control-Option-D on the Mac, and the agent does what you ask with it, or with no ask puts it away in a note, a to-do for today, or a reminder, and says where.
- An archive for the app: a session unused for 3 days, with nothing waiting in it, leaves the feed for the archive, and the app can archive one by hand. A new message brings it back. The agent archives sessions with `den sessions archive` when you ask it to tidy the feed.
- Backups: `den backup setup` backs up den's state, such as its sessions, jobs, and secrets, every 6 hours and before each `den update`, to a folder on your computer and an S3 bucket if you like. Each backup is encrypted to a key that stays on your computer, so neither den nor the agent can read it. `den backup restore` brings one back. [Backups](docs/backup.md) has the details.
- The app shows what the agent remembers about you, the Soul, User, and Notes sections of its memory, and lets you edit them. den commits the change to the space.
- A turn that fails before the agent takes a step offers Retry, in the app and in Telegram, which sends the same message again; one that fails later offers Continue, which asks the agent to go on.
- The app's stop button stops the turn of the session it is in, and leaves the others running. `/stop` in Telegram still stops everything.
- Today in the app, behind a calendar button in its bar: the agent's plan for your day in `~/space/today.md`, to-dos and things at a set time, such as an interview at 15:00, which you tick off in the app. At midnight den drops the ticked items and the past day's timed ones.
- The app keeps what you type as a draft, so it comes back after the app closes or crashes, and on the phone it stays when you move between screens. Attached files are not kept.
- The app shows the agent's reply as it writes it, instead of all at once when the turn ends. What the agent says before a step does not flash by, and a short reply comes whole.
- `den job edit` changes a job's title, schedule, model, or prompt in place, so it keeps its runs. The agent used to remove the job and add it again.

### Fixed

- Replies from OpenCode's models no longer end early, and an unreachable models.dev no longer slows every turn (fx den.9).
- Deleting a session in the app stops the turn that runs there, instead of refusing until it ends.
- A renamed job shows its new title in the app at once, on its past runs too, instead of after its next run.
- A session deleted in Telegram, by the agent, or as a job run with nothing to say leaves the app at once, and one deleted while the app was away leaves its archive.
- A picture sent to a session whose model cannot see pictures no longer fails the turn: the message goes on without it, and the agent learns where the picture is saved.

### Removed

- A reply that is exactly SILENT is sent like any other; the agent stays quiet with `den quiet`.

## v0.15.0

### Added

- The agent can read and edit your Google Drive, Docs, and Sheets. It changes files only you can open without asking; it asks before it changes a file other people can open, puts a file in a folder they can open, deletes one for good, mentions someone in a comment, or makes any other change, such as sharing a file or publishing it to the web. For edits to a file other people can open, Always and 1 hour allow edits to that file alone. To add Drive, enable the Drive, Docs, and Sheets APIs in your Google Cloud project, as the [guide](docs/services.md#google) says, and run `den connect google` again.
- On Google's sign-in page you can leave Gmail, Calendar, or Drive unchecked, and den uses only what you allowed. Before, den refused the connection unless you allowed both mail and calendar.
- The apps can delete a session. One whose turn runs stays until the turn ends or you stop it.

### Changed

- For Gmail and Calendar, den now lets through only the changes it knows stay with you, and asks about the rest, as it does for Drive and GitHub. An event in a calendar other than your primary one, even with no one else on it, now waits for your Allow, and so do deleting a draft or a label and requests den does not know, such as importing mail or watching for changes.
- Files and photos you send, from the app or Telegram, show in the app as files of your message that you can open and save, not as a line with their name. den keeps a copy for the app beside the agent's in its inbox.
- A message from the app takes up to 20 MB of files, as Telegram does, instead of 700 KB.
- A message you send while a turn runs joins that turn in every session, not only in the one Telegram's chat is in: in a session from the app, a job's, or a /bg task's too. Before, it waited and got an answer of its own after the turn's.

### Fixed

- A turn on Grok no longer fails with `InvalidGrokSubscriptionAccount` when it takes a subagent's report after the turn that started it ran longer than about 30 seconds. den now uses fx 0.0.17-den.7.
- The agent no longer reaches for a web search tool den does not have, and no longer holds back its pick when it lists options and you asked it to name one. den now uses fx 0.0.17-den.8.

## v0.14.0

### Changed

- `den service add` without arguments asks for the API's name, address, header, which requests wait for you, a note for the agent, and the token, one at a time, so you need no flags.
- `den connect` without arguments lists what you can connect. `den connect google` without the client JSON asks for its path and links the guide.
- Ask the agent what you can connect, or how to connect an API, and it answers with the command to run on your computer, with the API's address, header, and a note on its endpoints filled in from its docs.
- `/start` and `/help` in Telegram suggest a few first messages.
- The README is short: install, first messages, and what to connect. The details moved to guides in `docs/`.
- The end of `den setup` suggests a few first messages to the bot and how to connect more.

- When one task asks you to allow several actions, den tells the agent what came of them together, once you answered the last, so you get one reply with the result. The agent no longer stays silent after an action you allowed.

### Fixed

- A session you talk to only in the app no longer answers in Telegram when den wakes it, such as when a `den map` it started ends. Telegram gets a session's replies once you wrote to it there (a message, a reply to one of its messages, or `/bg`), whoever starts the turn; the app gets every session's.
- How far a `den map` run is shows in the app, under the session that started it, and in Telegram only when that session answers there. A run started from the app used to show its progress in Telegram alone, so the app looked as if the agent had stopped answering.
- After a `den map` run ends, the agent tells you what it found. It used to take den's report for something you had seen, and stay silent.

## v0.13.0

### Removed

- `den notify` is gone. The agent answers in its reply, so a chat turn sends one message instead of a stray extra one.

### Changed

- After an action you allowed went through, den tells the agent it needs no reply that only confirms it, so you get fewer "done" messages.
- A job's notification asks the agent for the result only, without what it did to get it.

## v0.12.0

### New

- A chart's data may have `null` where there is no data, such as an hour with no calls. The app and the picture leave a gap there instead of drawing values that were not measured.
- `den job log --json` prints a job's runs as JSON.
- The app's session list and a job's result name the job, so the app can mark den's own nightly jobs, and `GET /sessions?job=` lists one job's runs, so the app can show them all.
- `GET /sessions` and `GET /sessions/{id}` take `?limit=` and `?before=` and then answer a page with where the next one starts, so the app can load old sessions and long chats a page at a time. Without `?limit=` they answer as before.

### Changed

- When the agent sends a picture without a chart's data, `den send` reminds it to add `--widget`, so the app draws more charts itself instead of showing a picture.
- den stops gracefully: on `den update`, `den restart` or `den stop` it starts no new turns or jobs and waits up to 5 minutes for the ones that run. Messages sent meanwhile wait for the new den. A turn still running after that is stopped, and its session says so; a job says it failed because den restarted.
- While a turn runs, what the agent says before a step, such as "Reddit gives a stub, trying the old site", shows under that step in Telegram's progress message; den used to drop it.
- The agent stays quiet with `den quiet` instead of the word SILENT, so den no longer guesses from the reply's text whether to send it. A reply that is exactly SILENT still sends nothing until 2026-12-03, and den logs each one; the nightly tidy rewrites job prompts and skills in the space that still ask for the word.
- den no longer brings over what a den older than v0.10.0 kept in files. To update from one, update to v0.11.0 first.

### Fixed

- The app shows what each running turn does after it comes back to the foreground; it used to drop the step of every turn outside the chat's own session.
- The app no longer misses or doubles messages: den reconnects an app that falls behind instead of dropping events, and each message has a key the app keeps it once by.
- The app says when den or the app needs an update to understand the other.
- Two messages alike sent while a turn runs, such as "ok" twice, no longer get mixed up, and one fx did not take is always run as a turn of its own.
- The app shows a /bg task's session as a task, not as a chat.

## v0.11.0

### New

- The app can start a new task in a session of its own with one message, while the sessions it already has keep their turns.

### Fixed

- A file the agent sends, such as a chart, in a turn of a session other than the one the app or Telegram is in, such as a new task's or a reply to a job, now comes with that turn's reply instead of ahead of it on its own, and a turn started in the app no longer sends it to Telegram too.

## v0.10.1

### Fixed

- den map on Grok, and titles of sessions on Grok: the Grok subscription refused them for not saying which Grok CLI sent them.

## v0.10.0

### New

- Charts in the app: the agent describes a line or bar chart as data and sends it with its picture, so Telegram shows the picture and the den app draws the chart itself, in its own theme. Other files the agent sends show under its reply in the app too.
- The files the agent sends in a turn, such as charts, arrive with its reply instead of before it, one by one: in the app they are part of the reply, and in Telegram they follow it, the pictures together as one album. A turn you started in the app sends its files only there.
- den keeps its sessions in a database of its own, `den.db` in its state, and brings over the ones it had. A new session has no title until den makes one from your first message, in its language; a /bg task gets its title the same way.
- Jobs and their runs live in den's database too. A job's name is 2 or 3 English words joined by "-", such as `morning-brief`, and it has a title in your language, which you see above its messages and in /jobs; the agent gives both when it adds a job. den's own jobs are now `nightly-reflect` and `nightly-tidy`, and older jobs keep their names, or get one made from it.
- Jobs, /bg tasks, and replies to their messages run the way the chat does, in a session den names before it starts. A reply to "Background task started" goes on in the task once it ends, and what a job or a task reports shows in its session in the app at once.
- /compact, and den's own compacting while you are away, ask fx to compact the session as it is, without a turn of the model and without restarting the agent, and say how big it was and is. /compact compacts a session of any size. While you are away, den compacts the chat's session once it is past 60k tokens, whatever the model's window. This needs the fx fork's den.6 build.
- Sessions no longer wait for each other: a reply to a job's message, or a message after /new while the old session still works, runs at once on an agent of its own, and only a message for a session that is working gets 👀.
- den reads how big a session is from fx's own log of it, so /status and compacting while you are away see that session's size even while a job or another session runs beside it.
- What came of an action that waited for you, such as a git push, an email, a request to a service, or a den map run, goes to the session whose agent took it, a job's too, however many sessions run at once. den no longer guesses: it refuses such an action from an agent that names no session den has.
- The agent is told, right when an action starts waiting for you, that you already see den's question, so a turn with nothing else to say ends without a reply that only repeats it.
- A question from a job, a /bg task, or another session than the chat's says which above it, as their replies do, and keeps saying it once you answer.
- `den map` runs on a Grok subscription too, when you name its model, such as `--model grok/grok-4.5`, as on a ChatGPT or Claude one. Titles made with Grok's quick model go to it the way fx's requests do.

### Fixed

- A provider you signed in to, such as `opencode-go`, no longer vanishes from fx's settings when den starts, which left /new without its model and /model with "unknown configured provider". den changed fx's settings from several places at once, and wrote them anew when a read failed; it now changes them one at a time and leaves them as they are when it cannot read them. Sign in to the provider again once if it is gone.
- A job that runs `den map` waits for it and gets its result, instead of ending at once while the run went on in den's background and its result came to you as a notice.
- A reply in Telegram is marked as coming from another session only when it does: the app no longer moves Telegram's chat to the session it opens, so after /new your next reply no longer comes headed with an old session's title.
- A job run that wrote to you keeps its session, so a reply to its message goes on there however many runs come after it.
- The app stays paired on every device: `den app pair` makes a link for one more device, such as a phone and a laptop, rather than shutting out the one before. `den app unpair` still shuts every device out.
- A message stays in the session it was sent to. den names a new session when you start it, rather than after its first reply, so a message sent to a new session while another one works, or just before you switch, no longer lands in the other session or leaves the app waiting. This needs the fx fork's den.5 build.

## v0.9.1

### Fixed

- Pictures and files sent from the den app reach the agent: den saves them in the inbox as Telegram's, and the model sees the pictures. A message can carry files without text.

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
