---
name: reflect
description: Review the conversations since the last review and update memory and skills from what the user corrected, taught, or asked to remember. Use when a den job asks you to reflect.
---

# reflect

You learn from past sessions here. Most days there is little or nothing to save, and that is a good outcome.

## Steps

1. List the sessions that changed since the last review, or in the last day when there was none:

   ```bash
   den sessions recent --after .reflect-stamp
   ```

   Skip the runs of den's own jobs: their first message names the den job nightly-reflect or nightly-tidy, or "reflect" before it was renamed.

2. If there are no sessions, run `touch .reflect-stamp` and `den quiet`.

3. Read each session. The user's messages carry most of the signal: corrections, "remember", "always", "don't", and frustration with how something was done.

4. Decide what to change, using the rules below. Read a skill or section before you edit it.

5. Make the edits. Then check each line you added, and delete it when an answer is no:

   - A User line: can you quote the user's own words that say it?
   - Will it still be true and useful in a month?
   - Is it a method that worked, and not a setup failure, a tool that "does not work", or a story of one session?
   - Is it new, and not already in a built-in skill or another note?

6. Commit only the paths you changed, since other sessions may have work in progress here, then stamp:

   ```bash
   git add -- <paths> && git commit -m "reflect: <what changed>"
   touch .reflect-stamp
   ```

7. Reply with one to three short lines on what changed, such as "User: prefers short answers. Skill trip-planning: check visa rules first." Write them in the language the user writes to you in, since the reply goes to them. If nothing changed, run `den quiet` instead.

## Where a lesson goes

- The Soul section of `AGENTS.md`: who you are, such as your name, gender, voice, and manner. Save only what the user said or confirmed, and write it about you, never about the user.
- The User section of `AGENTS.md`: who the user is and what they want everywhere. Save only what the user said or confirmed. Never infer traits from tone, topic, or the tools used. A one-off request is not a preference unless the user marked it general. Write "the user", never "he" or "she".
- The Notes section of `AGENTS.md`: facts about the environment that matter in every session.
- A skill in `.fx/skills/<name>/SKILL.md`: how to do a kind of task for this user, including the steps, the commands that work, the user's preferences for the result, and pitfalls.
- `notes/skills/<name>.md`: what a built-in skill, one in `~/.agents/skills`, lacks or gets wrong here. Built-in skills are read-only, and a skill of your own must never share a built-in's name.
- `notes/`: one file per topic with what you know, such as decisions, plans, and lists, kept current in place.

One fact goes to one place. Keep the memory sections within their limits by merging or deleting stale lines.

Sessions may have saved a skill or a finding already, so check before you add one. Delete a line in `notes/skills/` that the built-in skill now says itself.

## Writing a skill

- Name a skill for a kind of task, such as `trip-planning`, never for one incident or date.
- Prefer to extend an existing skill over creating a new one. The same lesson learned twice is one rule.
- Write steps in order with the concrete commands. Attach each pitfall to its step as a rule with a short reason.
- Fix a wrong sentence in place instead of adding a correction below it.
- Give the skill a `description` that says when to use it. It is all a future session sees before it opens the skill.
- A wrong result is fixed in the rule or step that caused it, not with a new step that checks the old one.
- Keep a skill under about 600 words: it is a recipe, not a log of past runs.

## Do not save

- Failures caused by setup, such as a missing program or credential. The user can fix those, so they are not rules.
- Claims that a tool does not work. They turn into refusals long after the cause is fixed.
- Errors that went away on retry.
- A story of what happened in one session.
- Attempts that never worked. Save a method only when it succeeded.
- What the user is busy with now, such as testing or building den. It is not who they are.

## Local findings

Findings about this skill on this machine are in `notes/skills/reflect.md`. Read it before you start, and add there what will still hold on the next run, never what this run did or found. This skill itself is read-only.
