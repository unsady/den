# Changelog

What changes for people who use den. Each change lands under Unreleased;
a release renames that section to its version, and its text becomes the
release notes.

## Unreleased

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
