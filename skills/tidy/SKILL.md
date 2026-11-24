---
name: tidy
description: Keep the space easy to search: fold logs and reports in notes/ into topic notes, stop the jobs that write them, shorten long skills, and keep each note's description. Use when a den job asks you to tidy.
---

# tidy

You keep `~/space` easy to find things in. Most nights there is little or nothing to do, and that is a good outcome.

## Steps

1. Look at what is there and what changed since the last tidy:

   ```bash
   cd ~/space
   find notes -name '*.md' | sort
   git log --since="$(date -r .tidy-stamp 2>/dev/null || date -d '7 days ago')" --name-status --format= -- notes .fx/skills | sort -u
   ```

2. Find logs and reports: a file named for a date or a run, or one that a job adds on every run. Notes hold topics, not logs or reports of runs. For each kind:

   - Find the job that writes it with `den job ls` and read the job's prompt and skill.
   - Change the skill so it writes no such file, and returns the result in its reply instead.
   - When the job's prompt asks for the file or carries rules of its own, move those rules into the skill and replace the job with one whose prompt only names the skill and mode, with the same name, schedule, and slot. The `den` skill explains jobs.
   - Fold what still matters from the files into the topic's note, and delete them.

3. Rewrite each skill over about 600 words, which `wc -w .fx/skills/*/SKILL.md` shows: merge rules, and drop the history of past runs, such as their dates and results. Keep the limits that rules set.

4. Check the notes' descriptions. Each note starts with frontmatter whose `description` says what the note holds in one phrase of at most 15 words:

   ```markdown
   ---
   description: How to present den in posts and images
   ---
   ```

   Add a missing one, shorten a longer one, and fix one that no longer matches the note. When two notes cover the same topic, merge them into one.

5. Commit only the paths you changed, since other sessions may have work in progress here, then stamp:

   ```bash
   git add -- <paths> && git commit -m "tidy: <what changed>"
   touch .tidy-stamp
   ```

6. Reply with one to three short lines on what changed, such as "Folded the news-daily reports into notes/news.md; the job no longer writes them." Write them in the language the user writes to you in. If nothing changed, run `den quiet` instead.

## Do not

- Change what a note says beyond merging and removing logs: facts and the user's decisions stay.
- Delete data a job reads, such as a database or a script, or anything outside `notes/` and `.fx/skills/`.
- Change a job's schedule, slot, or what it does.

## Local findings

Findings about this skill on this machine are in `notes/skills/tidy.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
