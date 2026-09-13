# Repository instructions

## Git conventions

- Use Conventional Commits for every commit and PR title: `type(scope): description` (scope is optional).
- Use types such as `feat`, `fix`, `docs`, `refactor`, `test`, `perf`, `build`, `ci`, and `chore`.
- Keep descriptions short and describe the resulting change. Example: `feat(streaming): add live camera recording and forwarding`.
- The primary branch is `main`. Name working branches `codex/<type>/<short-kebab-case-description>`, for example `codex/fix/receiver-reconnect` or `codex/perf/reduce-video-buffering`.
- Never include generated credentials, TLS private keys, connection QR codes, recordings, runtime state, or private measurement artifacts in commits. Keep these in the ignored local directories.

## Working on the lab

- Use snake_case for command directories under `cmd/`, such as `net_check`, `relay_check`, and `video_lab`, consistent with Go source file names.
- Application and diagnostic commands are written in Go. FFmpeg and MediaMTX handle media processing. Do not introduce a Python application dependency.
- Preserve running camera connections, recordings and uploads when performing unrelated repository work.
- Use `go test ./...` and `go vet ./...` for Go changes. Saved-video comparison tests run with `node --test cmd/quality_check/page_test.mjs`; the live viewer is GStreamer.
- Full media acceptance uses `./lab test`. It exercises isolated generated streams and takes longer; run it for relevant media behavior changes, not documentation-only edits.
- Explain system choices and measured limits in plain English. Do not present short local tests as proof of production reliability or a guaranteed maximum delay.
