# Contributing

## Build and test

```sh
go build ./cmd/whyd
go vet ./...
go test ./...
```

The tests run on real journals recorded in `testdata/` (an out-of-memory freeze, a quiet
unclean end, a clean power-off, a reboot with a suspend), anonymised. `whyd --file` opens
them too:

```sh
go run ./cmd/whyd --file testdata/boot-unclean-oom.jsonl.gz
```

`WHYD_DUMP=dir go test ./internal/ui` writes every screen of the interface to `dir`, as
plain text and with colours, at each tested size.

whyd reads only part of each boot (warnings and errors, the programs the rules name, logins,
the last hour). After changing the rules or the analysis, check on your own journal that the
result is the same as reading every line:

```sh
WHYD_LIVE=1 go test -run TestSelection ./internal/analyze
```

## Rules

The most useful contribution. Start from the lines whyd lists as other warnings (`W` in the
interface, "Other warnings" in `whyd report`), write a rule with at least one real example
line and one that must not match, and check it with `whyd rules mine.toml`. The format and the
guidelines are in [docs/RULES.md](docs/RULES.md).

## A journal that whyd gets wrong

Record it and attach it to an issue, after checking what's inside:

```sh
journalctl -b -1 -o json --all --output-fields=MESSAGE,PRIORITY,SYSLOG_IDENTIFIER,_COMM,_PID,_SYSTEMD_UNIT,_TRANSPORT,_BOOT_ID | gzip > boot.jsonl.gz
```

A journal contains user names, host names, network names and addresses: remove what you
don't want to share before attaching it.

## Code

- Plain Go and the standard library first; the interface uses Bubble Tea v2 and Lip Gloss v2.
- `gofmt`, `go vet` and the tests must pass.
- Commits: one line, conventional style (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`).
- On screen: short factual wording in plain words (error, warning, abrupt end), compact numbers
  (17h22m, 7.5G), terminal colours only and only where they mean something (red errors and bad
  ends, yellow warnings), bold only for the verdict, no decorative symbols, keyboard only.

## Releases

Pushing a tag builds the release on GitHub (`.github/workflows/release.yml`, configured in
`.goreleaser.yaml`): binaries for amd64 and arm64, Arch, Debian and RPM packages, checksums.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

`goreleaser release --snapshot --clean` builds the same files locally in `dist/`.

By contributing you agree that your work is released under the
[GPL-3.0-or-later](LICENSE).
