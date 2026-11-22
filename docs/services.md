# Services

You connect a service in a terminal on the computer where den runs, never in the chat: den asks for the token or password there and keeps it from the agent. Not sure how? Ask the agent in Telegram, such as "what can I connect?" or "help me connect Todoist": it answers with the command to run, filled in from the service's docs.

| What | Command |
|---|---|
| [Your own API](#your-own-api) | `den service add` |
| [GitHub](#github) | `den secret set github_token` |
| [Google: Gmail, Calendar, Drive](#google) | `den connect google` |
| [Any mailbox](#any-mailbox) | `den connect mail` |
| [Web search](#web-search) with higher limits | `den secret set exa_key` |

## Your own API

Any API that takes a token in a header:

```bash
den service add
```

den asks for a short name, such as `crm`, the API's https address, the header for the token (Enter keeps `Authorization: Bearer`), which requests wait for your Allow, a note for the agent on what the API does and where its docs are, and last the token. The same in one line, for a script or the agent's suggestion:

```bash
den service add --header X-Api-Key --about "Deals API: GET /api/deals, POST /api/deals; docs at https://crm.example.com/docs" crm https://crm.example.com
```

The agent then calls `den fetch --secret crm https://crm.example.com/api/deals`, and den adds the token, only for `crm.example.com`. The agent reads your note with `den service ls`, so the more it says about the endpoints, the fewer tries the agent needs.

Reads go at once and writes wait for your answer in Telegram. `--ask deletes` asks only before a delete, `--ask never` lets the agent write on its own, and `--ask always` asks about every request. `den service rm crm` removes the service and its token.

What den cannot add this way:

- An API that signs in with OAuth, or with a login and password.
- An API that takes the token in the address, such as `?key=`.
- A service over plain http, such as one on your home network: den sends tokens only over https.

## GitHub

Create a fine-grained token limited to the repositories the agent may use, with Contents, Issues, and Pull requests set to read and write, and run `den secret set github_token`. The agent clones and pushes with plain `git`, and calls GitHub's API with `den fetch`. Creating a branch and changing a branch the agent created go at once, and so do issues and pull requests through the API. Changing any other branch, such as `main`, merging, deleting a branch, pushing a tag, writing to someone else's repository, changing who can access one, or creating a public one waits for your answer.

A branch the agent pushes runs your repository's GitHub Actions that start on a push or a pull request, with the secrets those workflows get. So leave out of the token any repository whose workflows hold secrets the agent must not reach, and give the token no Workflows permission, so the agent cannot change workflows. Where tags publish something, such as a release, protect them with a tag ruleset in the repository's settings. Ask the agent to back up its workspace, and it pushes `space/` to an empty private repository of yours every night.

## Google

den reaches Gmail, Google Calendar, and Drive with its Docs and Sheets. It uses an OAuth client from your own Google Cloud project, so no one else holds access to your account. In the project:

1. Enable the APIs you want den to use: Gmail API, Google Calendar API, and for files Google Drive API, Google Docs API, and Google Sheets API.
2. Under Data Access, add their scopes: `gmail.modify`, `calendar.events`, `drive`.
3. Under Audience, publish the app. In Testing, Google expires the access in 7 days.
4. Create a client of type Desktop app and download its JSON.

Then run `den connect google`, and give it the JSON's path. It opens Google's sign-in; Google warns that the app is unverified, and under Advanced you continue to it. If you sign in on another device, the last page fails to load: paste its address into the terminal. An app for personal use needs no verification from Google, and `den disconnect google` revokes the access.

Google's page lists mail, calendar, and Drive, each with a checkbox: leave unchecked what den should not reach, and den uses only what you allowed. To add one later, run `den connect google` again.

den asks before mail goes out, before an event with other people is created or changed, before anyone gets a calendar notification, and before an event is deleted, and shows the recipients, subject, and text. In Drive, it asks before a file is shared or its access changes, before anything is deleted for good, and before a comment that may mention someone, who then gets an email. Reading, labels, archiving, trash, drafts, your own events, and editing, creating, and copying files need no answer. People you already share a file with see the agent's edits; a file's version history keeps what an edit replaced.

## Any mailbox

`den connect mail` takes an address and an app password instead of Google sign-in, and checks the login. den knows the servers of Gmail, iCloud, Fastmail, Yahoo, AOL, Zoho, and GMX, and asks for others. The agent uses IMAP and SMTP on den's local ports, and den logs in to your server with the password the agent never sees. Each message sent waits for your Allow. An app password opens the whole mailbox; Google sign-in grants narrower access and covers the calendar.

## Web search

`den search` works without setup, through the free endpoints of Exa, Keenable, and Parallel. A key gives higher limits: `den secret set exa_key`, or `deepseek_key`, `tavily_key`, or `brave_key`, and den asks that service first. A service that hits a limit rests for an hour, or a day when it runs out of quota, and den moves to the next. No service gets anything that names you.
