# tram

**Your ssh_config, with eyes.**

*tram* is Vietnamese for a station: a place you stop on the way. A `ProxyJump`
chain is a line of them.

## What tram is not

- **Not a terminal emulator.** Your terminal is better than anything I could
  write. `enter` hands the wheel to `ssh` running in the real terminal. This is
  also why there is no tab strip inside tram: drawing one would mean rendering
  the session under it, which means a VT parser, scrollback, resize and mouse
  forwarding, and getting any of it wrong breaks vim. The tabs belong to your
  terminal, and tram opens them for you.
- **Not a private connection store.** There is no `connections.yaml`. Delete
  tram and `ssh web1` still works. This is the largest single difference from
  most tools in this category.
- **Not a background service.** No daemon, no agent of its own, no listening
  port.
- **Not a sync tool.** No vault, no server, no account. Syncing is git's job, or
  Syncthing's, on your own `ssh_config`.

## What it is

A viewer and a safe editor for `~/.ssh/config`, plus a handful of commands that
run in parallel across many hosts.

```
tram                       open the interface
tram web1                  connect
tram web1 -- uptime        run one command and exit
tram ls --wide             list hosts, with accounts and drift
tram ls web1               one host, with its ProxyJump route resolved
tram add web2 --addr 10.0.0.2 --account deploy --group prod/web
tram mv bastion jump1      rename, and fix every ProxyJump that named it
tram ping -g prod          OK / AUTH / REFUSED / TIMEOUT / DNS / HOST_KEY
tram doctor web1           walk the route, stop at the first broken station
tram doctor --config       check ssh_config itself
tram exec -g prod -- df -h run a command across a group
tram import inventory.ini  read an Ansible inventory or a CSV export
tram web1 -v               connect, with ssh narrating its own progress
```

## When a host will not open

ssh sets no connect timeout of its own and prints nothing while it waits, so a
machine that is not answering looks exactly like a hung program. Two commands
answer it.

`tram doctor web1` tries every station on the route in turn and stops at the
first that fails, naming it and what kind of failure it was. It uses a timeout,
so it answers in seconds rather than minutes.

`tram web1 -v` runs the real connection with ssh's own narration, which says
which address it is still waiting on. Repeat the flag for more.

Pressing ctrl+c only reaches ssh while it is still connecting: once a session is
open the key goes to the program on the far side. So an interrupt means it never
got in, and tram says where to look next.

Every command that reads something takes `-f json`, `-f yaml`, `-f csv` or
`-f value`. Every command that writes something takes `--dry-run` and `--diff`,
and prints the diff before touching anything.

## Install

```bash
go build -o tram .
```

No cgo. One binary.

## The six things it actually does

**1. `ssh_config` is the only source of truth.** tram writes into it directly.
A file read and written back is byte-for-byte identical: `Host *`, `Match`,
`Include`, your comments, your indentation, your line endings, a missing final
newline. Comments and unrecognised directives inside a stanza belong to that
stanza and are deleted with it, rather than drifting to the top of the file
where they would become global settings. Writes go through a temporary file and
a rename, with a `.tram.bak` beside the original.

**2. Accounts are identities, not connections.** An account holds a user name,
an authentication method and a key path. It holds no address, no port and no
jump host. Linking a host to an account writes `User` and `IdentityFile` into
that stanza once. Edit the stanza by hand afterwards and tram marks it as
drifted, keeps the link, and never overwrites your edit.

**3. It understands ProxyJump.** It resolves the whole route, warns which hosts
break before you delete a station, rewrites every reference when you rename one,
and detects loops. That last one matters more than it sounds: given a circular
`ProxyJump`, ssh does not report an error, it hangs. tram refuses the route and
says which stations form the ring.

**4. Failures are classified, not flattened.** `ping` answers `OK`, `AUTH`,
`REFUSED`, `TIMEOUT`, `DNS`, `HOST_KEY`, `JUMP` or `CONFIG`, because each one
sends you somewhere different. `doctor` walks a route one station at a time and
stops at the first that fails, instead of reporting that a chain of three
machines "could not connect".

**5. A passphrase is typed once per run, and stored nowhere.** There is no
keyring, no vault and no saved password. When ssh asks for a key passphrase,
tram asks you, and holds the answer for the rest of the run so that the other
hosts sharing that key file do not ask again. Quit tram and it is gone. A host
key confirmation is refused permanently: answering "yes" to an unknown
fingerprint on your behalf would turn a warning about interception into a
silent accept. The question is put to you instead, with ssh's own wording and
fingerprint, and whatever you type goes straight back.

## Typing a key passphrase once

Open a host whose key is passphrase protected, type the passphrase when ssh
asks, and every other host in that run using the same key file connects without
asking. Quit tram and the next run asks again. Nothing is written to a keyring,
a vault or a configuration file.

The answers cannot simply live in memory, because the helper ssh runs is a
separate process. They live in a file only that run can read: the file holds
ciphertext, the key exists only in the tram process and the children it starts,
and the file is deleted on the way out. A crash leaves bytes nobody can decrypt
rather than a passphrase on disk.

When ssh asks anything else through the helper, a host key confirmation for a
machine you have not met before, the question is shown to you as ssh wrote it
and your answer is passed back unchanged. tram decides nothing there and
remembers nothing.

It engages only where it can work, and where it would otherwise be worse than
nothing. ssh with the helper forced does not fall back to asking on its own, so
tram arms it only when there is an encrypted key to ask about, an OpenSSH new
enough for `SSH_ASKPASS_REQUIRE` (8.4), a console to ask on, and a person
sitting at it. In a script it stays out of the way entirely.

Passwords are not tram's business. A host that authenticates with one is left
to ssh, which asks exactly as it always did. Set `reuse_passphrase = false` in
`config.toml` to leave passphrases to ssh as well.

`tram key ls` shows which of a host's keys are passphrase protected, which is
the question behind "why is it asking me again". If you own the far end, a key
installed with `tram key push` beats typing anything.

**6. The numbers match the tools you would check them against.** Not yet built;
see the roadmap.

## Importing

`tram import` reads an inventory written for another tool. Three formats, and
the format is detected from the file unless `--from` says otherwise: the classic
Ansible INI inventory, the Ansible YAML inventory, and a CSV export with a
header row.

Only the Ansible variables that say how to reach a machine are read:
`ansible_host`, `ansible_user`, `ansible_port`, `ansible_ssh_private_key_file`,
and a jump host given as `-J` or `-o ProxyJump=` inside
`ansible_ssh_common_args`. Everything else in an inventory is about what Ansible
does once it has connected, which is none of tram's business. A `ProxyCommand`
is deliberately not translated into a `ProxyJump`: a command is not a host, and
guessing at one produces a stanza that looks right and connects somewhere else.

Group nesting comes across. A host in `webservers`, where `webservers` is a
child of `prod`, lands in `prod/webservers`. A host in several groups keeps the
most specific one, and the rest are reported rather than dropped in silence.
Host ranges such as `web[01:04]` are expanded, zero padding and all.

`--group` replaces all of that with one group of your own. `--group-prefix`
keeps the file's structure and nests it under yours instead.

An inventory often names machines without saying how to log in to them.
`--user`, `--key`, `--jump` and `--account` set those on every host in one pass,
so they do not have to be edited in afterwards one at a time. The import screen
has the same fields.

A host Ansible reaches some other way, say `ansible_connection=local`, is
skipped and named. ssh cannot be pointed at it, so importing it would only
create a stanza that fails.

Nothing is written until you say so. `--dry-run` shows the plan, an existing
host is left alone unless you pass `--overwrite`, and re-importing the same file
reports that everything already matches.

In the interface, `I` does the same thing. Press enter on the file field to walk
the folders rather than typing a path from memory: directories open, files are
chosen, and the ones tram can read are listed first. Then read the preview and
press `w` to write it.

## Where things live

```
~/.ssh/config                 the source of truth. tram reads and writes it.
~/.ssh/config.d/tram.conf     tram's own file, after `tram init`
~/.ssh/*.tram.bak             a snapshot taken before each write

~/.config/tram/               (or %APPDATA%\tram\)
├── accounts.json             identities, and which host is linked to which
├── snippets.json             saved commands
├── history.json              last connected, and pinned hosts
├── facts.json                what the last measurements found: the round trip,
│                             the failure when there was one, and the load,
│                             disk, memory and system of each host
└── config.toml               tram's own options

```

Nothing above holds a secret. A key passphrase lives only for the run that
typed it.

Groups, descriptions and tags are kept as comments inside the stanza, in tram's
own spelling, and the group and description are also read from the comment run
above the `Host` line and in the spelling used by sshfleet, the tool tram
replaces. An existing
configuration therefore keeps its groups on the first run, and the next edit to
a host migrates its markers to tram's spelling without leaving a second copy.

Delete `~/.config/tram/` entirely and `tram ls` still lists every host and
`tram web1` still connects. You lose Recent, Favorites, snippets, account
links and every measurement. Groups, descriptions and tags survive, because they
are kept in `ssh_config` as comments.

After `tram init`, hosts declared outside `config.d/tram.conf` are read-only:
still listed, still connectable, still usable with `exec`, but writing to one
needs `--force`.

## The interface

Three screens, drawn as one console: a bar naming the program and the file, the
panes, and a bar of keys with a status line.

**Hosts.** The table in the middle, a group tree on the left, the details of
whatever the cursor is on the right. The columns are the alias, the login and
address, the port, the latency, when you last connected, and the tags. `s`
changes which column the table is sorted by and `S` reverses it. The window
decides how many columns there is room for: the latency goes first, then the
port, then the tags, then the date, and the alias is the last to give ground.
`tab` moves between the tree and the table, `g` puts the tree away and `i` puts
the details away.

`enter` connects, `f` opens sftp, `y` copies the ssh
command, `space` marks hosts, `/` searches, `a`/`e`/`c`/`d` add, edit, clone and
delete, `E` edits everything marked, `A` links to an account, `x` runs a
command, `r` runs a snippet, `D` runs the doctor, `I` imports an inventory, `*`
pins, `?` lists the keys.

`ctrl+k` opens the command palette: every action in one list, by name, filtered
as you type. Nothing in it is implemented twice. Each entry replays the key that
already does the job, so a palette entry cannot drift away from the key it
claims to be, and neither can the right-click menu.

**Tabs and panes.** `W` opens a tab in the terminal tram is already running in,
one for each marked host, and `V` opens a pane beside the list instead. Neither
gives up the screen: the terminal does the work, so the list is still there when
the tab appears, and the bar says what was opened. On Windows Terminal these are
`wt -w 0 new-tab` and `wt -w 0 split-pane`; under tmux they are a window and a
split. Elsewhere `V` falls back to a window and says so. More than six tabs at
once asks first, because marking forty hosts and brushing `W` should not cost
you forty tabs.

**The mouse** works, and the design is why: a row selects, the box at its left
end marks, a column heading sorts by that column and sorts back when clicked
again, a tab switches view, a group selects and a second click on the same group
opens its branch, the wheel scrolls, a double click connects, and the right
button opens a menu for the row under the pointer. The chips along the bottom
and the buttons in the panes are clickable too, and each is labelled with the
key that does the same thing.

Capturing the mouse takes selection and pasting away from the terminal, which is
a real loss. tram hands it back the moment a box takes typing: while a form, the
search or the palette is open, the pointer belongs to the terminal again, so
right-click and middle-click paste into a field the way they always did. Over a
list it belongs to tram. Most terminals also give selection back while you hold
shift. If you would rather not have it at all, put `mouse = false` in
`config.toml`.

**Sessions** is the same table over the hosts you have actually opened, most
recent first. It is for getting back to what you were doing: the machine you
were on this morning is at the top of it, rather than somewhere in three hundred
alphabetical rows.

**Keys** lists your identities rather than your machines: for each one, the
login it uses, how it proves itself, which key file, and how many hosts are
linked to it. It is where you go to answer "which key is this host using" and
"what else uses it". `enter` or `e` edits one, `a` makes one, and renaming one
moves every host linked to it.

`1`, `2` and `3` switch between the three tabs, and so does clicking them.

**Measuring.** Nothing in the latency column or the readings under it is a
guess. `p` measures the selection and `P` measures everything shown: one ssh
connection per host, which times the round trip and, in the same connection,
asks the machine what it is, how loaded it is, and how full its disk and memory
are. Every one of those is a read. A host that answers but has none of those
commands, such as a switch, counts as reachable and leaves the fields empty. A
host nobody has measured shows a dash, never a zero.

What comes back is written to `facts.json`, so it is still there on the next
run. The sweep itself runs off the main loop, so the interface stays usable
while hundreds of connections are attempted, and the bar says how many are
still going. What the last sweep found sits in the bar along the bottom: so
many up, so many slow, so many down.

**The diagnosis** is the second half of the details pane, and it is a reading
rather than a chart. tram measures when you ask it to, so a series over time is
a series of two or three points; what helps instead is the last answer said in
words. It names the class of failure, explains what that class means, prints
the line ssh itself wrote, and draws the route with each station's own last
reading beside it. A destination that times out behind a station that is
refusing connections therefore explains itself, without anything being probed
again. `D` walks the route one station at a time when that is not enough.

The line that matters most on a jump station is in the block above it: **used
by** names the hosts that route through this one, so what breaks if it does is
on the screen before you delete or rename it.

The bar at the top also says what the ssh agent is holding. That reading is
`ssh-add -l` and its exit status, asked once when the interface opens; the keys
themselves are never shown, only how many there are.

**Tags** are free labels kept beside the stanza as `#tram-tags:`. A group says
where a host sits in a hierarchy and a host has one; tags say what a host is and
it can have as many as it needs. A search starting with `#` matches both.

The account, group and jump fields open a list rather than asking you to
remember what exists, and the account list can make one: filling in a host and
finding the identity does not exist yet no longer costs you the half-filled
host, because the account form opens on top and hands back to it. The key field
opens the folder, the same as the import screen's.

**Results.** One block per host. A handful of hosts opens with the output
already showing; more than that opens collapsed, because forty blocks of output
is not a screen anyone can read, and `space` expands one while `o` expands them
all. `enter` connects to whichever host is selected. An import preview uses the
same screen, where `w` writes the hosts and `esc` throws the plan away.

The work itself runs off the main loop. Ping, doctor and exec are ssh to every
host in the selection, which is seconds at best and a hung connection at worst,
and the bar says what it is waiting for while they run.

The group tree carries three views above the hierarchy: **Favorites** for what
you pinned, **Recent** for what you have opened, and **Marked** for what an
action is about to be applied to. Marked appears as soon as there is one.

Pasting works in every box that takes typing: with ctrl+v, and with the middle
button or however else your terminal pastes. Selecting text is the terminal's
own while the mouse is captured, which on most terminals means holding shift,
and is unconditional with `mouse = false`.

The colours are the SSHFleet Console design in `giaodien/`, hex for hex. On a
terminal that cannot show sixteen million colours they are converted to the
nearest it has, and `--ascii` swaps the box drawing and the sparkline blocks for
characters a console from 1995 will print. tram does not paint the page
background: it draws on the terminal's own, so a themed or transparent terminal
keeps looking like itself.

The interface draws tram's own data and nothing else. It never renders the
contents of a session. `enter` exits the interface, gives the terminal to ssh,
and starts the interface again when ssh is done.

## Testing

Three layers, the first of which is the one that matters:

1. **Differential `ssh -G`.** For every file in a corpus of awkward
   configurations and every host in it, ssh is asked what settings it would use
   before and after tram rewrites the file. The diff must be empty. The oracle
   is ssh itself, not tram's idea of what a parser should do.
2. **Golden files** for the writer: add a host, delete one with odd comments in
   it, rename one with a cascade.
3. **Unit tests** for the failure classifier, using the wordings OpenSSH
   actually produces, and for the importer, against inventories in the shapes
   people really write them.

The askpass mechanism has a test of its own, because the whole feature rests on
a claim about ssh that a version number does not prove. It generates a real
encrypted key, starts a real agent, and checks that `ssh-add` with no standard
input at all calls the helper and uses what it says.

```bash
go test ./...
```

## A note about Windows

OpenSSH for Windows silently ignores an `Include` whose path begins with a drive
letter. It resolves as if the file did not exist, with no warning. `tram init`
therefore writes a relative path, and so should you.

## Roadmap

Built: the parser and writer, the handoff, `ls`, the editing commands, `init`,
accounts, secrets and askpass, key handling, the full ProxyJump treatment,
`ping`, `doctor`, `exec`, `key push`, `import`, and the two screens.

Not yet built: `tram top` and its dashboard, `tram tmux`, and the ASCII fallback
beyond the `--ascii` flag.

Deliberately not planned: anything that redraws the inside of an ssh session,
tabs, split panes, prefix keys, session recording, a hand-drawn SFTP browser, or
a tunnel manager.
