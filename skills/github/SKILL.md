---
name: github
description: Clone and push the user's GitHub repositories with git, and work with issues, pull requests, and files through the GitHub API with den fetch. Use for any task on GitHub.
---

# github

den holds the user's GitHub token and adds it to each request, to git's and to the API's. You never see it, so never ask the user for it, try to read it, or put one in a URL.

```bash
den fetch --secret github https://api.github.com/user
```

- When den answers that `github_token` is not set, tell the user GitHub is not connected yet. They create a fine-grained token at github.com/settings/personal-access-tokens, limited to the repositories you need, with Contents, Issues, and Pull requests set to read and write, and run `den secret set github_token` on their computer.
- Public repositories also work without the token.

## git

git works as usual with `https://github.com/<owner>/<repo>` addresses: den's git config sends them through den, which adds the token. Clone into the space, change many files, commit, and push, rather than changing files one by one through the API.

```bash
git clone https://github.com/<owner>/<repo>
git switch -c <topic-branch>
git commit -am "…"
git push -u origin <topic-branch>
```

- Commit as the user only once they told you their name and email; until then ask, and keep the answer in `notes/skills/github.md`. Set it per repository with `git config user.name` and `git config user.email`.
- A push that creates a branch, or changes a branch you created through den, goes at once, force-pushes included. A push to any other branch, such as `main`, deleting such a branch, or pushing a tag waits for the user's Allow in Telegram. So work on your own branch and open a pull request, unless the user asked you to push to `main`.
- Such a push fails at once with `den: waiting for the user in Telegram`: den keeps it and sends it to GitHub once the user allows it, then tells you in a new `[den:` message. Do not push it again; after that message, `git fetch` updates your copy of the remote branch. If the branch moved on GitHub meanwhile, GitHub refuses the kept push, and den says so: fetch, rebase or merge, and push again.

## Back up the space

The space is a git repository, and the nightly review commits to it. When the user asks for a backup, push it to a private repository of theirs every night:

1. Ask the user to create an empty private repository, without a README, and to give their GitHub token access to it. An empty one matters: your first push then creates its branch, so later pushes go without a question.
2. Check that it is private, since the space holds the user's notes: `den fetch --secret github https://api.github.com/repos/<owner>/<repo>` shows `"private": true`. Then add it as `backup` and push:

   ```bash
   git -C ~/space remote add backup https://github.com/<owner>/<repo>
   git -C ~/space push backup HEAD:main
   ```

3. Add a job after the nightly review that stays quiet unless the push fails:

   ```bash
   den job add --name backup --schedule "30 4 * * *" "Push the space to its backup with git -C ~/space push backup HEAD:main. Reply exactly SILENT when it succeeds; otherwise say why in one line."
   ```

The backup holds the space only, not `archive/` or `inbox/`.

## What waits for the user in the API

Reads go at once, and so do these changes to the user's own repositories: issues, comments, labels, milestones, opening and editing pull requests, and git blobs, trees, and commits. den asks the user in Telegram first before any other write: files changed through the API, merges, tags, releases, workflow runs, deletions, access, anything in someone else's repository, a public repository, and any GraphQL mutation. To change files, commit and push with git to your own branch, and open a pull request. Such a request returns at once, saying den asked: den sends it once the user allows it and tells you the service's answer in a new `[den:` message. The user already sees den's question, so do not tell them you asked, and do not send it again.

## Requests

`den fetch` prints the response body; `-i` adds the status and headers. A body given with `-d` makes it a POST unless `-X` says otherwise, and `-d @file` reads a file.

```bash
gh=https://api.github.com
den fetch --secret github "$gh/repos/<owner>/<repo>/issues?state=open&per_page=50" | jq -r '.[] | "#\(.number) \(.title)"'
den fetch --secret github -d '{"title":"…","body":"…"}' "$gh/repos/<owner>/<repo>/issues"
den fetch --secret github -d '{"body":"…"}' "$gh/repos/<owner>/<repo>/issues/<n>/comments"
den fetch --secret github -H "Accept: application/vnd.github.raw+json" "$gh/repos/<owner>/<repo>/contents/<path>"
den fetch --secret github "$gh/search/issues?q=<query>+repo:<owner>/<repo>"
```

- **Change a file:** `PUT /repos/<owner>/<repo>/contents/<path>` with `message`, the new content in base64 (`base64 -w0 file`), and, for an existing file, its current `sha` from a GET of the same path.
- **Open a pull request:** `POST /repos/<owner>/<repo>/pulls` with `title`, `head`, `base`, and `body`.
- **Get a private repository's files:** download `GET /repos/<owner>/<repo>/tarball/<branch>` to a file and unpack it with `tar -xzf`.
- **More pages:** a long list continues in the `Link` header (`-i`) as `rel="next"`. Raise `per_page` up to 100 before you page.

## Local findings

Findings about this skill on this machine are in `notes/skills/github.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
