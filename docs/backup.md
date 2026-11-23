# Backups

den's state lives in its volume: the sessions den knows, the chat, jobs, approvals, and secrets. `den backup setup` backs it up every 6 hours and before each `den update`, to a folder on your computer and, if you like, to an S3 bucket. Each place keeps the latest 14 backups.

```
den backup setup
```

It asks for the folder, by default `~/.local/share/den/backups`, and for the bucket, then restarts den so the container sees the folder, and takes the first backup. If a backup fails, den tells you in Telegram, and again once backups work.

## The key

Every backup is encrypted with [age](https://age-encryption.org) to a key that stays on your computer, in `~/.config/den/backup-key.txt`. den holds only its public half, so neither den nor the agent can read a backup: the folder is shared with the container, and a shared folder does not keep permissions there. The agent could delete a backup in the folder, but not change one unnoticed; the bucket is the copy it cannot reach.

**Keep a copy of the key somewhere safe, such as a password manager.** Without it no backup can be read. A backup also opens with the `age` tool: `age -d -i backup-key.txt den-….tar.gz.age | tar tz`.

## A bucket

Any S3-compatible service works, such as AWS S3, Cloudflare R2, or Backblaze B2. Make a private bucket and an access key that can read, write, list, and delete in it, then give `den backup setup` the service's endpoint, such as `https://s3.us-east-1.amazonaws.com` or `https://<account>.r2.cloudflarestorage.com` (region `auto`), the bucket, and the key. den keeps the key as the secret `backup_s3`.

## Restore

```
den backup ls
den backup restore den-20261123T090000Z.tar.gz.age
```

`restore` takes a backup's name in the folder, or the path to one, such as a file you downloaded from the bucket. It opens the backup with the key first, so a damaged backup or the wrong key changes nothing, then asks, stops den, replaces its state, and starts it again. `--key <file>` takes the key from another file, as on a new computer.

What den downloads again, such as Claude Code and the voice model, is not in a backup. Neither is the agent's folder, with its notes and sessions: it is a folder on your computer, so your computer's own backup, such as Time Machine, keeps it.
