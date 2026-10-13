<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/logo/logo-dark.svg">
  <img src="docs/logo/logo-light.svg" alt="whyd" height="96">
</picture>

**Why did my PC stop?**

[![Release](https://img.shields.io/github/v/release/vmaltarello/whyd?style=flat-square&logo=git&logoColor=white)](https://github.com/vmaltarello/whyd/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/vmaltarello/whyd/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white&label=CI)](https://github.com/vmaltarello/whyd/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/vmaltarello/whyd?style=flat-square&logo=go&logoColor=white&color=00ADD8)](go.mod)
[![License](https://img.shields.io/github/license/vmaltarello/whyd?style=flat-square&color=blue)](LICENSE)
[![Linux](https://img.shields.io/badge/Linux-systemd-30D475?style=flat-square&logo=linux&logoColor=white)](#install)

</div>

whyd reads the journal of the previous boot and tells you, in one screen, how it ended and
what went wrong before.

![whyd: the sessions on the left, the selected one and its problems on the right](docs/screenshots/main.png)

Your laptop froze, you held the power button, it came back up. The answer is in the journal,
buried under thousands of lines of routine noise. whyd finds it:

- **how it ended**: clean power-off or reboot, hard power-off, died asleep, kernel panic
- **what went wrong before**: out-of-memory kills, GPU errors, crashes, failed services,
  hung tasks, overheating, disk errors, each with a short explanation
- **the last minutes** of the session, with the hidden lines folded away
- **a report** to paste on a forum, with addresses, hostname, user and Wi-Fi names removed

Works on any Linux distribution with systemd.

## Install

whyd is a single program with no dependencies besides systemd's `journalctl`. Pick the line
for your system (the links always point to the latest release; for ARM replace `amd64` with
`arm64`):

```sh
# any distribution: downloads the latest release, checks it, installs to /usr/local/bin
curl -fsSL https://raw.githubusercontent.com/vmaltarello/whyd/main/install.sh | sh

# Arch Linux and derivatives (downloaded first: pacman wants a signature for a package
# installed from a link, not for a local file)
curl -fLO https://github.com/vmaltarello/whyd/releases/latest/download/whyd_linux_amd64.pkg.tar.zst
sudo pacman -U ./whyd_linux_amd64.pkg.tar.zst

# Debian, Ubuntu and derivatives
curl -fLO https://github.com/vmaltarello/whyd/releases/latest/download/whyd_linux_amd64.deb
sudo apt install ./whyd_linux_amd64.deb

# Fedora (openSUSE: sudo zypper install with the same link)
sudo dnf install https://github.com/vmaltarello/whyd/releases/latest/download/whyd_linux_amd64.rpm

# from source, with Go 1.26 or newer
go install github.com/vmaltarello/whyd/cmd/whyd@latest
```

Without sudo, the script installs to `~/.local/bin` with `... | PREFIX=~/.local sh`. To
update, run the same line again.

Reading the system journal needs a user in the `systemd-journal`, `adm` or `wheel` group
(the first user usually is, on Ubuntu and Arch); otherwise run whyd with `sudo`.

## Use

```
whyd                 the previous boot, interactive
whyd -b -3           another boot (same numbers as journalctl -b; 1 is the oldest)
whyd list -u         the sessions of the last 30 days that didn't shut down cleanly
                     (--days N or --all to go further back)
whyd --text          the summary as plain text
whyd report          a Markdown report, redacted, ready to paste
whyd --file F        one boot recorded on another machine
                     (journalctl -b -1 -o json --all > F, - for stdin)
```

In the interface the arrows do everything: ↑ ↓ move, → and ← switch panel, `Enter` opens a
problem's journal lines. `L` shows the last minutes, `R` the report, `?` every key.

<table>
<tr>
<td><img src="docs/screenshots/tail.png" alt="The last minutes before the end"></td>
<td><img src="docs/screenshots/report.png" alt="The report, redacted"></td>
</tr>
<tr>
<td align="center">The last minutes</td>
<td align="center">The report</td>
</tr>
</table>

## Rules

Problems are recognised by rules written in TOML, not code. Your own go in
`~/.config/whyd/rules/` and can add to, change or turn off the built-in ones. Found a problem whyd
didn't recognise? A rule with a real example line is the best contribution:
see [docs/RULES.md](docs/RULES.md).

## Documentation

- [Rules](docs/RULES.md): the rule format and the built-in rules
- [Contributing](CONTRIBUTING.md): building, testing, sending changes

## License

[GPL-3.0-or-later](LICENSE)
