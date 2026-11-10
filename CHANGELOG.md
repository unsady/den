# Changelog

What changes for people who use den. Each change lands under Unreleased;
a release renames that section to its version, and its text becomes the
release notes.

## Unreleased

- Setup and `den login` take an Anthropic, xAI, or Vercel AI Gateway API key, through each one's OpenAI-compatible API.
- `/status` shows the context size with a ChatGPT subscription too: den read the wrong token count from Codex's answers.
- Setup's list of other providers starts with the popular ones, such as OpenRouter, DeepSeek, OpenCode, Z.AI, and Moonshot AI.
- `den update` also updates the `den` command itself from the latest release, after checking its checksum. Before, only the image was updated, and a new `den` needed the install line again.
- The web skill tells the agent to read every page with `web_fetch`, which turns HTML into clean text, and to keep `curl` for APIs, downloads, and requests with headers.

## v0.1.1

- `den setup` asks which account pays for the model: OpenCode Go, OpenRouter, DeepSeek, a ChatGPT Plus or Pro subscription, or any other provider on models.dev. A model is ready for each, and lists filter as you type.
- Setup's questions take arrow keys and masked input, and each finished step shows ✓ or ✗. A wrong bot token asks again instead of ending the setup.
- Setup checks that den's container reaches the internet and points at a VPN when it does not.
- `install.sh` works on macOS again: its `sh` stopped at the download message in a UTF-8 locale.
- An Always rule now covers every item of a path with long ids such as ULIDs, not only the one approved.
- A ChatGPT subscription connected first becomes den's model.

## v0.1.0

- First public release.
