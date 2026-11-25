# Voice messages

den transcribes the voice messages you send, so the agent reads what you said. It needs one of the two setups below.

## In the container

Pick a [whisper.cpp model](https://huggingface.co/ggerganov/whisper.cpp) and your language, and den restarts with them:

```sh
den config set DEN_WHISPER_MODEL small-q8_0 DEN_WHISPER_LANGUAGE en
```

den downloads the model once and starts `whisper-cli` for each voice message, so the model takes memory only while it works. On a five-second message on arm64:

| Model | Cores | Time | Peak memory |
|---|---|---|---|
| `large-v3-turbo-q8_0` (874 MB) | 4 | 8 s | 1.1 GB |
| `large-v3-turbo-q8_0` | 2 | 15 s | 1.1 GB |
| `small-q8_0` (264 MB) | 2 | 4 s | 0.5 GB |

`large-v3-turbo` handles speech that mixes languages better. `DEN_WHISPER_LANGUAGE=auto` detects the language but runs the model twice.

## Through a service

A service with OpenAI's transcription API works instead: for Groq, run `den config set DEN_TRANSCRIBE_URL https://api.groq.com/openai/v1/audio/transcriptions DEN_TRANSCRIBE_MODEL whisper-large-v3-turbo` and `den secret set transcribe_key`.
