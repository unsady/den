---
name: data
description: Keep records that change and get filtered, such as job openings with a status, a reading list, or a log of prices, in a SQLite database. Use when a list outgrows a markdown note.
---

# data

`sqlite3` is installed. A database is one file, so put it in `~/space/.data/`, such as `~/space/.data/jobs.db`, which git ignores, and query or change it with one command:

```bash
sqlite3 ~/space/.data/jobs.db "UPDATE jobs SET status = 'applied' WHERE id = 42"
sqlite3 -box ~/space/.data/jobs.db "SELECT id, company, status FROM jobs WHERE status = 'new' ORDER BY added DESC LIMIT 20"
```

Small lists the user reads, such as a packing list, stay markdown in `notes/`.
