---
name: media
description: Get the subtitles, transcript, or details of a YouTube video, podcast, or other online video or audio with yt-dlp, and transcribe audio locally. Use when the user shares a video or audio link or asks what a video says.
---

# media

Keep downloads in `~/media/`.

## yt-dlp

Install it once, if `command -v yt-dlp` finds nothing:

```bash
mkdir -p ~/.local/bin
case "$(uname -m)" in aarch64) asset=yt-dlp_linux_aarch64 ;; *) asset=yt-dlp_linux ;; esac
curl -fsSL -o ~/.local/bin/yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/latest/download/$asset"
chmod +x ~/.local/bin/yt-dlp
```

Sites change often. When yt-dlp fails to extract a video, run `yt-dlp -U` and try once more. A warning about a missing JavaScript runtime is harmless for subtitles, details, and audio.

## Details and subtitles in one request

YouTube limits how often a server may ask, so get everything you need in one call:

```bash
yt-dlp -J "<url>" | jq '{title, duration, channel, language,
  subtitles: (.subtitles // {} | keys), auto: [.automatic_captions // {} | keys[] | select(endswith("-orig"))]}'
```

`subtitles` lists the author's tracks, and `auto` the automatic ones in the video's own language. Both empty means there are no subtitles.

## When YouTube blocks the server

An HTTP 429 or "Sign in to confirm you're not a bot" means YouTube is limiting this server for a while. Do not open the video in Lightpanda or retry right away: both meet the same block. Get the title from `curl -s "https://www.youtube.com/oembed?url=<url>&format=json"`, tell the user YouTube is limiting requests, and offer to try again later with a den job in about 30 minutes that reports the result.

## Subtitles first

Subtitles take seconds, so try them before any transcription.

1. Pick a track from the details above. Prefer the author's subtitles in the video's language over automatic ones.
2. Download exactly one track:

   ```bash
   yt-dlp --skip-download --write-subs --write-auto-subs --sub-langs en --sub-format vtt -o "$HOME/media/%(id)s.%(ext)s" "<url>"
   ```

   Name one language, never a pattern such as `en.*`: a pattern pulls dozens of machine translations and YouTube answers with 429.
3. Turn the VTT file into plain text:

   ```bash
   sed -e '/-->/d' -e '/^WEBVTT/d' -e '/^Kind:/d' -e '/^Language:/d' -e 's/<[^>]*>//g' file.vtt | awk 'NF && !seen[$0]++'
   ```

## Transcribe when there are no subtitles

How long transcription takes depends on the machine and the model, so measure it on the first minute instead of guessing. A turn and a den job each end after 30 minutes.

The local model is set by `DEN_WHISPER_MODEL`. When that variable is empty, there is no local model: tell the user.

ffmpeg converts the audio. Install it once, if `command -v ffmpeg` finds nothing:

```bash
curl -fsSL "https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-$(dpkg --print-architecture)-static.tar.xz" | tar -xJ -C /tmp
cp /tmp/ffmpeg-*-static/ffmpeg ~/.local/bin/ && rm -rf /tmp/ffmpeg-*-static
```

1. Download the audio and transcribe its first minute, timing it:

   ```bash
   cd ~/media
   yt-dlp -f bestaudio -o "%(id)s.%(ext)s" "<url>"
   ffmpeg -loglevel error -t 60 -i <id>.<ext> -ar 16000 -ac 1 <id>-start.wav
   model="$DEN_STATE/models/ggml-$DEN_WHISPER_MODEL.bin"
   s=$(date +%s); whisper-cli --no-timestamps --language <lang> --threads "$(nproc)" --model "$model" \
     --file <id>-start.wav > <id>-start.txt 2> <id>-start.log; echo "$(( $(date +%s) - s )) seconds"
   ```

   Set `<lang>` to the video's language from the details, or `auto` when it is unknown; then `grep -o "auto-detected language: [a-z]*" <id>-start.log` names it for the full run. Never use `DEN_WHISPER_LANGUAGE` here: it is the user's language for voice messages, and a wrong language garbles the text.

2. Estimate the whole: the seconds the minute took, times the audio's length in minutes. The first minute already tells you what the video is about.

3. Decide whether to go on:
   - If the user's memory says how to handle long tasks, such as "always finish without asking", follow it.
   - Otherwise go on when the estimate is under about 5 minutes. When it is longer, tell the user the estimate and what the first minute says, and ask whether to transcribe the rest.
   - When the estimate is over 25 minutes, it will not fit in one turn: offer a part instead, such as a time range the user picks.
   - When the user answers with "always" or "from now on", save that as their preference in memory.

4. Transcribe the rest:

   ```bash
   ffmpeg -loglevel error -i <id>.<ext> -ar 16000 -ac 1 <id>.wav
   whisper-cli --no-prints --no-timestamps --language <lang> --threads "$(nproc)" --model "$model" \
     --file <id>.wav > <id>.txt
   ```

- `ffmpeg -ss <start> -to <end>` cuts a time range.
- Run one transcription at a time: each takes several hundred MB of memory.
- Delete the `.wav` files and the audio when done, and keep the `.txt`.

## Local findings

Findings about this skill on this machine are in `notes/skills/media.md`. Read it before you start, and add what you learn there. This skill itself is read-only.
