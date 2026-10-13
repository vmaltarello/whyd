# Rules

whyd recognises problems in the journal with **rules**: short blocks of text that say "a line
like this one means this problem". The built-in rules come with whyd; you can write your own
for the messages it doesn't know yet, and change or turn off the built-in ones, without
touching any code.

- [How rules work](#how-rules-work)
- [Your first rule, step by step](#your-first-rule-step-by-step)
- [Going further](#going-further)
- [Reference](#reference)
- [Writing a good rule](#writing-a-good-rule)
- [Built-in rules](#built-in-rules)

## How rules work

whyd reads the journal of a session line by line, and tries the rules on each line, in order.
The first rule that recognises the line takes it, and its **level** says what happens:

- `err`, `warn`, `info`: the line is a problem in the list, with a title and an explanation;
- `context`: the line is taken but not listed, it is only context (the system suspended);
- `hidden`: the line is hidden and only counted ("437 firewall log lines").

If no rule takes a line and it is a warning or an error, whyd lists it as **not recognised**
(`W` in the interface). Those are the lines a new rule can explain.

## Your first rule, step by step

We'll write a rule for a real message whyd doesn't recognise yet.

### 1. Find the line

Open whyd, choose a session and press `W`: these are the warnings and errors no rule took.

```
┌─ Not recognised: 1 errors, 8 warnings, 9 messages ──────────────────────────────────────┐
│ PRIORITY  TIMES  FIRST      SOURCE          MESSAGE                                     │
│ warning   1      13:45      (udev-worker)   ACAD: Process '/bin/sh -c 'echo 75 80 > /s… │
│ warning   1      13:45      (udev-worker)   cfg80211: Process '/usr/bin/set-wireless-r… │
```

Select a line to read it in full:

```
cfg80211: Process '/usr/bin/set-wireless-regdom' failed with exit code 1.
```

Two things matter: the **message** (the text) and the **source**, the program that logged it,
here `(udev-worker)`. PRIORITY is how serious the program said the line was. The message says that a command started by a udev rule failed.

### 2. Write a rule that recognises it

Rules go in TOML files in `~/.config/whyd/rules/`; any name ending in `.toml` will do. Create
`~/.config/whyd/rules/mine.toml` with:

```toml
[[rule]]
id    = "mine.udev-command"
level = "warn"
text  = "failed with exit code"
title = "a udev command failed"
```

Line by line:

- `[[rule]]` starts a rule. A file can hold as many as you like, one after the other.
- `id` names the rule: unique, `area.what-it-is` in lowercase letters, digits and dashes. The
  part before the dot is the rule's area (`mine` here; `gpu`, `disk`, `memory` in the built-in
  rules).
- `level`: how serious **you** judge the problem: `"err"`, `"warn"` or `"info"`, as whyd shows
  it. It doesn't depend on the PRIORITY of the line.
- `text`: the text to look for in the line, anywhere in it.
- `title`: what the problem is called in the list.

Run `whyd --text` (or open whyd) on that session:

```
TIME          LEVEL  WHAT                                                 TIMES
Wed 13:45     warn   a udev command failed                                2
```

Both lines are recognised, and since they have the same title they are one problem that
happened 2 times.

### 3. Say which program logs it

"failed with exit code" could come from any program. Add `source` to limit the rule to one:

```toml
source = "(udev-worker)"
```

Write it exactly as in the SOURCE column, parentheses and capitals included. Kernel messages
have the source `kernel`.

Always give the source when you know it: whyd doesn't read every line of a long session, but
it does read every line of the programs named in a `source` (more in [What whyd
reads](#what-whyd-reads)).

### 4. Show what's in the line

The title would be more useful with the device and the command. Write the whole line in
`text`, with a **`{name}`** where it changes, and use the names in the title:

```toml
text  = "{device}: Process {command} failed with exit code {code:num}"
title = "{device}: udev command failed"
```

`{device}` takes the message from its start up to `: Process`, `{command}` what comes up to
` failed`, and `{code:num}` a number. Everything else is plain text, matched as written:

```
TIME          LEVEL  WHAT                                                 TIMES
Wed 13:45     warn   ACAD: udev command failed                            1
Wed 13:45     warn   cfg80211: udev command failed                        1
```

The titles are now different, so they are two problems. `{source}` is always available too.
[Text](#text) lists the other placeholders.

### 5. Explain it

Two more fields say what the problem means. Add them to the rule:

```toml
note    = "{command} exited with code {code}"
explain = """
A udev rule ran a command when the device appeared or changed, and the command failed.
What the rule was there to do, such as a setting or a permission, was not done.
"""
```

`note` is one short line; `explain` is a few sentences, and the `"""` let it span several
lines. This is where each field appears in whyd:

```
┌─ Problems: 4 warn, 6 info ─────────────────────────────────────┐
│ TIME       LEVEL  WHAT                                   TIMES │
│ 13:45      warn   ACAD: udev command failed                  1 │
│ 13:45      warn   cfg80211: udev command failed              1 │  ← level, title, times
│ 13:45      warn   NetworkManager error                       1 │
│                                                                │
│ '/usr/bin/set-wireless-regdom' exited with code 1              │  ← note
│                                                                │
│ A udev rule ran a command when the device appeared or          │  ← explain
│ changed, and the command failed. What the rule was there to    │
│ do, such as a setting or a permission, was not done.           │
└────────────────────────────────────────────────────────────────┘
```

The same title, level and note go into `whyd --text` and into the report.

### 6. Test it

Add the real line as an example, and a similar line the rule must leave alone:

```toml
match   = ["cfg80211: Process '/usr/bin/set-wireless-regdom' failed with exit code 1."]
nomatch = ["cfg80211: Process '/usr/bin/set-wireless-regdom' succeeded."]
```

Then run:

```
$ whyd rules test
ok: 53 rules, 68 examples
```

`whyd rules test` checks every rule: mistakes in the file, examples that don't do what they
say, and rules that can never fire. If something is wrong it tells you what:

```
SKIP /home/you/.config/whyd/rules/mine.toml: rule "mine.udev-command" skipped: unknown field "sorce", did you mean "source"?
```

A rule with a mistake is skipped; whyd keeps working with the others.

### The whole rule

```toml
[[rule]]
id      = "mine.udev-command"
level   = "warn"
source  = "(udev-worker)"
text    = "{device}: Process {command} failed with exit code {code:num}"
title   = "{device}: udev command failed"
note    = "{command} exited with code {code}"
explain = """
A udev rule ran a command when the device appeared or changed, and the command failed.
What the rule was there to do, such as a setting or a permission, was not done.
"""
match   = ["cfg80211: Process '/usr/bin/set-wireless-regdom' failed with exit code 1."]
nomatch = ["cfg80211: Process '/usr/bin/set-wireless-regdom' succeeded."]
```

Rules are read every time whyd starts: save the file and run whyd again.

## Going further

### Several texts

`text` can be a list: the rule takes a line that matches any of them.

```toml
text = [
  "BTRFS error (device {device})",
  "BTRFS critical (device {device})",
]
```

### Change or turn off a built-in rule

Write a rule with the **same `id`** as a built-in one (`whyd rules` lists them all) and only
the fields you want to change; the others stay as they are:

```toml
[[rule]]
id    = "access.denied"
level = "info"     # quieter

[[rule]]
id    = "known.rtw88-tx"
level = "off"      # turned off
```

`whyd rules` then shows `(changes built-in)` next to it, and a rule turned off with the level
`off`. A list you write, such as `text`, replaces the built-in one. A changed rule keeps its place among
the built-in rules; your new rules come before all of them (see [Order](#order)).

### Hidden lines

Some lines are harmless but so frequent that they bury everything else. `level = "hidden"`
hides them and counts them ("Hidden: 437 firewall log lines"); in the last minutes they are
folded, and `N` shows them.

```toml
[[rule]]
id     = "hidden.firewall"
level  = "hidden"
source = "kernel"
text   = "IN={_} OUT={_} SRC="
title  = "firewall log lines"
```

The title says what the lines are, for the count. Hide sparingly: a hidden line is a line
nobody reads. For something harmless but worth knowing, prefer `level = "info"`.

### Context, not a problem

`level = "context"` takes a line without listing a problem: it is not "not recognised"
anymore, and it is marked `*` in the last minutes. It is for lines that only give context
(the system suspended) or that repeat another problem (systemd reporting the crash that
systemd-coredump already reported). A context rule needs no title.

### Problems on several lines: until

Some problems are reported over many lines: an out-of-memory report is a header, the state of
the memory, one line per process and the process killed. With **`until`**, one occurrence is
the whole block, from the line of `text` to the line of `until`:

```toml
[[rule]]
id        = "memory.oom-report"
level     = "err"
source    = "kernel"
text      = "{trigger} invoked oom-killer"
until     = "Out of memory: Killed process {pid:num} ({process})"
max_lines = 5000
title     = "out of memory, {process} killed"
```

- The block takes the lines of the same source after the first one, up to and with the line
  of `until`. Names from both are available in the title.
- It also ends after `max_lines` lines (300 if not given), a minute after its first line, or
  when another first line begins a new block. Then `until` was never seen and its names are
  missing: give `max_lines` more than the longest real block.
- `Enter` on the problem in whyd shows every line of the block.

### Many occurrences, one problem

Occurrences with the same title are one problem, counted in TIMES, over the whole session.
Two fields change that:

```toml
merge_title  = "out of memory, {count} processes killed"
merge_window = "10m"
```

- `merge_title`: every occurrence of the rule joins one problem, even with different titles;
  once there are two or more, this is its title, and `{count}` is how many.
- `merge_window`: only join an occurrence that comes within this time of the previous one
  (`"30s"`, `"10m"`, `"1h"`); later ones start a new problem.

### Only the serious lines: priority

Every journal line has a priority, given by the program that logged it (the PRIORITY column of
`W`), the one `journalctl -p` filters on: `emerg`, `alert`,
`crit`, `err`, `warning`, `notice`, `info`, `debug`, from the most serious. `priority =
"warning"` limits a rule to warnings and worse, `"err"` to errors and worse. Useful when a
program logs the same words in harmless lines too.

## Reference

### Fields

| Field | Required | Values |
|---|---|---|
| `id` | yes | unique, `area.name` in lowercase letters, digits and dashes; the area is the part before the first dot. The `id` of an existing rule changes that rule |
| `level` | yes | `"err"`: can explain a crash or a freeze by itself; `"warn"`: worth a look; `"info"`: context, known harmless messages; `"context"`: taken, not listed; `"hidden"`: hidden and counted; `"off"`: turns off an existing rule |
| `title` | yes, except context | text with `{names}` |
| `text` | yes | the line to recognise, or a list of them: see [Text](#text) |
| `source` | no | the program, exactly as whyd shows it; kernel messages: `"kernel"`. With a source, `text` can be left out: the rule takes every line of that program |
| `priority` | no | `"emerg"` … `"debug"`: that priority or more serious |
| `note` | no | one short line under the title, with `{names}`; repeated problems show "from 10:08 to 11:35" instead |
| `explain` | no | sentences; a blank line starts a new paragraph; whyd shows the whole sentences that fit |
| `merge_title` | no | every occurrence joins one problem, with this title; `{count}` is how many |
| `merge_window` | no | join only within this time: `"30s"`, `"10m"`, `"1h"` |
| `until` | no | the last line of a block over several lines, written like `text` |
| `max_lines` | with `until` | a number, 300 if not given |
| `match` | recommended | real lines the rule must recognise: `["...", "..."]` |
| `nomatch` | recommended | close lines it must leave alone |

A field whyd doesn't know, a field that has no effect for the level, a wrong value or a
`{name}` the rule doesn't fill is a mistake: whyd skips the rule and says what to fix.

### Text

`text` is the line as it is, with placeholders where it changes. It can be anywhere in the
message; capitals matter.

| Write | Takes |
|---|---|
| `{name}` | the text up to what follows it in `text`; at the start, from the start of the message; at the end, the rest of the line |
| `{name:num}` | a number |
| `{_}` | anything, without keeping it |

- Everything else is matched as written: `(`, `[`, `.` and `*` are just characters.
- Put some text between two placeholders: `{a}{b}` can't say where one ends.
- Capitals matter: for a message written both ways, list both, as in
  `["permission denied", "Permission denied"]`.
- A message can hold several lines (stack traces): a placeholder doesn't cross a line break.

### Titles

`title`, `note` and `merge_title` can use `{name}` for a part of `text` or `until` (without
its type: `{pid}`, not `{pid:num}`), `{source}` for the program, and `{count}` (only in
`merge_title`) for how many occurrences were joined.

### Order

Your rules first, in the alphabetical order of your files, each from top to bottom; then the
built-in files in alphabetical order (`auth.toml`, `context.toml`, …). On each line the first
rule whose `source`, `priority` and text all fit takes it: your rules see every line before
the built-in ones. A built-in rule you change keeps its place. A block that a rule with
`until` is collecting gets its lines before any other rule.

### What whyd reads

To open a session quickly, whyd doesn't read every line: it reads the warnings and errors,
every line of the programs named in some `source`, the logins, and everything in the last hour
before the end. A rule without `source` therefore sees info and debug lines only in that last
hour.

### Files

- Your rules: `~/.config/whyd/rules/*.toml` (`$XDG_CONFIG_HOME/whyd/rules/` if set). With
  `sudo`, whyd usually reads root's directory instead.
- The built-in rules: [`rules/`](../rules), compiled into whyd.
- `whyd rules` lists every rule, its level and the file it comes from.
- `whyd rules test` checks your rules; `whyd rules test file.toml` checks one file before
  you put it in place. It exits with status 1 when something is wrong.

## Writing a good rule

- **Start from a real line**, and keep it in `match`. Add to `nomatch` the closest line that
  must not be taken.
- **Give the source**, and a `priority` when the program writes the same words in harmless
  lines.
- **Write the part that doesn't change**, with `{name}` for what you want to show and `{_}`
  for what you don't.
- **Explain with facts**: two or three plain sentences on what the message means and what it
  can lead to. No guessed causes, no links, nothing specific to one distribution.
- **Choose the level honestly**: `err` only for what can stop or freeze the system by itself.
- **Share it**: if it helped you, propose it for [`rules/`](../rules) with its real line.

## Built-in rules

Context rules take their lines without listing a problem; hidden lines are counted.

| ID | File | Level | Title |
|---|---|---|---|
| `auth.failed` | auth.toml | warn | {service}: authentication failed |
| `auth.wrong-password` | auth.toml | warn | sudo: {n} incorrect password attempts |
| `auth.no-password` | auth.toml | info | sudo refused: a password is required |
| `auth.conversation` | auth.toml | context | (context) |
| `power.suspend` | context.toml | context | (context) |
| `power.resume` | context.toml | context | (context) |
| `power.hibernate` | context.toml | context | (context) |
| `power.thaw` | context.toml | context | (context) |
| `known.rtw88-tx` | context.toml | info | {driver} TX errors, known issue |
| `known.portal-gtk` | context.toml | info | portal-gtk failed at login, harmless |
| `known.rtw88-dpk` | context.toml | info | {driver} calibration failed (known issue) |
| `crash.coredump` | crash.toml | warn | {process} killed by SIG{signal} |
| `crash.coredump-detail` | crash.toml | context | (context) |
| `crash.coredump-lost` | crash.toml | info | a core dump could not be saved |
| `network.wifi-failed` | desktop.toml | warn | {device}: Wi-Fi connection failed |
| `audio.underrun` | desktop.toml | info | audio underruns ({client}) |
| `dbus.activation-failed` | desktop.toml | info | D-Bus service {name} could not be started |
| `dbus.service-file` | desktop.toml | info | D-Bus service file problem |
| `network.nm-error` | desktop.toml | warn | NetworkManager error |
| `bluetooth.error` | desktop.toml | info | bluetoothd error |
| `firmware.boot` | firmware.toml | info | firmware or hardware setup message |
| `firmware.tpm` | firmware.toml | info | TPM NvPCR not available |
| `gpu.amdgpu-timeout` | gpu.toml | err | amdgpu ring {ring} timeout |
| `gpu.amdgpu-reset` | gpu.toml | err | amdgpu GPU reset |
| `gpu.i915-hang` | gpu.toml | err | i915 GPU hang |
| `gpu.nvidia-xid` | gpu.toml | err | NVIDIA Xid {xid} |
| `gpu.amdgpu-error` | gpu.toml | warn | amdgpu error in {func} |
| `disk.ext4-error` | hardware.toml | err | ext4 error on {device} |
| `disk.btrfs-error` | hardware.toml | err | btrfs error on {device} |
| `disk.io-error` | hardware.toml | err | I/O error on {device} |
| `disk.nvme-timeout` | hardware.toml | err | NVMe {device} timeout, controller reset |
| `thermal.critical` | hardware.toml | err | critical temperature, emergency shutdown |
| `disk.fat-dirty` | hardware.toml | info | {device} not unmounted cleanly (FAT) |
| `hidden.firewall` | hidden.toml | hidden | firewall log lines |
| `hidden.rtw88-push` | hidden.toml | hidden | rtw88 push errors |
| `hidden.wireless-ext` | hidden.toml | hidden | wireless extensions warnings |
| `hidden.wpa-multicast` | hidden.toml | hidden | wpa_supplicant multicast notices |
| `kernel.panic` | kernel.toml | err | kernel panic: {reason} |
| `kernel.hung-task` | kernel.toml | err | {process} blocked for over {secs}s |
| `kernel.soft-lockup` | kernel.toml | err | CPU {cpu} soft lockup ({secs}s) |
| `kernel.hard-lockup` | kernel.toml | err | CPU {cpu} hard lockup |
| `kernel.bug` | kernel.toml | warn | kernel bug: {what} |
| `kernel.watchdog-running` | kernel.toml | info | {dev} still running when closed |
| `memory.oom-report` | memory.toml | err | out of memory, {process} killed |
| `memory.oom-kill` | memory.toml | err | out of memory, {process} killed |
| `memory.oomd-kill` | memory.toml | err | systemd-oomd killed {cgroup} |
| `memory.alloc-trouble` | memory.toml | warn | {process}: page allocation {what} |
| `service.dumped` | service.toml | context | (context) |
| `service.core-dump` | service.toml | context | (context) |
| `service.failed` | service.toml | warn | {unit} failed: {result} |
| `service.start-failed` | service.toml | context | (context) |
| `access.denied` | service.toml | warn | {source}: permission denied |

### How much they recognise

On the 42 sessions of the laptop whyd was developed on, this is where each warning and error
line ended up:

| Lines | Recognised by a rule | Hidden | Not recognised |
|---|---|---|---|
| Errors (priority 0 to 3): 4,645 | 2,675 (58%) | 1,964 (42%) | 6 (0.1%) |
| Warnings (priority 4): 10,828 | 4,238 (39%) | 5,440 (50%) | 1,150 (11%) |

Another machine, with other hardware and other programs, will have more lines not recognised:
each of them is a rule waiting to be written.
