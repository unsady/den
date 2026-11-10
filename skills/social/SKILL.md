---
name: social
description: Read posts, threads, and accounts on X (Twitter) and Reddit. Use when the user shares an x.com, twitter.com, or reddit.com link or asks what an account or community posted.
---

# social

These methods only read public data. Post, reply, like, or send a message only through an official API the user connected, and only after the user approved the exact text.

## X

x.com itself shows nothing without a login, so `web_fetch` and Lightpanda return an empty page there. Do not retry them; use the methods below.

### One post

The post id is the number in the link: `https://x.com/user/status/2119520873577398695?s=46` has the id `2119520873577398695`.

```bash
curl -s -m 20 "https://cdn.syndication.twimg.com/tweet-result?id=<id>&token=a"
```

The JSON has `text`, `created_at`, `user.screen_name`, `favorite_count`, `conversation_id`, `entities` for links, `mediaDetails[].media_url_https` for images and video, and `quoted_tweet`. It has no replies and no full thread.

- A deleted or wrong id returns an HTML page, not JSON. Check that the body starts with `{` before you parse it.
- In a post such as "what is going on here?", the meaning is usually in the image. Download `media_url_https` with `?name=orig` into `~/media/` and look at it.
- For several posts, request them one at a time with a pause of a second or two.
- When this endpoint fails, `curl -s -m 20 "https://api.fxtwitter.com/<user>/status/<id>"` returns `tweet.text`, `tweet.author`, `tweet.media`, and `tweet.quote`.

### An account's recent posts

```bash
curl -s -m 25 -o ~/media/x-<user>.html "https://syndication.twitter.com/srv/timeline-profile/screen-name/<user>?lang=en&showReplies=false"
grep -o '<script id="__NEXT_DATA__" type="application/json">[^<]*' ~/media/x-<user>.html | sed 's/^[^>]*>//' |
  jq -r '.props.pageProps.timeline.entries | map(.content.tweet | select(.))
    | sort_by(.created_at | strptime("%a %b %d %H:%M:%S %z %Y") | mktime) | reverse | .[:5][]
    | "\(.created_at) \(.id_str) \(.favorite_count) \(.full_text[:200])"'
```

- The entries are not in date order, and the first ones are often old popular posts, so always sort by `created_at`.
- This endpoint allows only a few requests: it answers `429` for a minute or more after three or four. Wait, try once more, and then tell the user.

### Official API

When `xurl` is installed and `xurl whoami` succeeds, the user has connected the X API: use `xurl` for search, threads, and replies. Otherwise do not suggest it unless the user needs something the methods above cannot read.

## Reddit

Add `.json` to a Reddit link to get its data:

```bash
curl -s -m 20 -A "den-agent/1.0 (personal assistant)" "https://www.reddit.com/r/<sub>/top.json?t=day&limit=10"
curl -s -m 20 -A "den-agent/1.0 (personal assistant)" "https://www.reddit.com/r/<sub>/comments/<id>.json?limit=50"
```

Posts are in `.data.children[].data`, with `title`, `selftext`, `score`, `num_comments`, and `permalink`. Reddit often answers servers with a bot check, an HTML page instead of JSON. Then tell the user that Reddit blocked the request; do not try to get around it.

## Local findings

Findings about this skill on this machine are in `notes/skills/social.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
