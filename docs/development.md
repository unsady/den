# Development

In a checkout, `go build -o ~/.local/bin/den ./cmd/den` builds `den`, and `den setup` run there builds the image from the checkout instead of pulling it; `den update` then rebuilds it, and the `den` command too when Go is installed. `scripts/release.sh` builds `den` for every system `install.sh` supports. Before a release that changes a prompt, `go test -tags eval -run TestReplies -v ./internal/runner` asks the model of the den that runs on your computer a few questions and checks its replies: short, in plain text, in your language, and `den quiet` when a job has nothing to send.

The image takes fx from a release of [a fork](https://github.com/unsady/fx) that carries the few changes den needs, and checks its digest. To try your own fx, build it in an fx checkout with `zig build -Dtarget=aarch64-linux -Doptimize=ReleaseSafe` and copy `zig-out/bin/fx` to `build/fx` before `den update`.
