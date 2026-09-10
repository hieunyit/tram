# tram

**Your ssh_config, with eyes.**

*tram* is Vietnamese for a station: a place you stop on the way. A `ProxyJump`
chain is a line of them.

## What tram is not

- **Not a terminal emulator.** Your terminal is better than anything I could
  write. `enter` hands the wheel to `ssh` running in the real terminal.
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
```

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

**5. Secrets live where the operating system keeps secrets.** The OS keyring,
with an encrypted file as a fallback. A value never reaches `ssh_config`, never
appears on a command line, never sits in an environment variable. ssh gets it by
calling tram back as its askpass helper. A passphrase is checked against the key
before it is stored. The helper answers exactly two questions and refuses host
key confirmations permanently: answering "yes" to an unknown fingerprint on your
behalf would turn a warning about interception into a silent accept.

## Typing a password once

The first time a host asks for a password, tram asks for it on the console,
hands it to ssh, and keeps it if the session opens. After that it answers the
prompt itself and you are not asked again.

Two details make this safe to leave switched on. The answer is parked under a
one-off identifier while the session runs and only written to the keyring once
the connection has actually succeeded, so a typo is never remembered. And the
host named in ssh's own prompt decides which secret is served, so a jump
station gets its own password rather than the destination's.

`tram secret ls` shows what is stored and where, never the values.
`tram secret rm <host>` forgets one. Setting `remember_secrets = false` in
`config.toml` turns the whole thing off and leaves every prompt to ssh.

It only engages when it can work: OpenSSH 8.4 or newer, and a console to ask
on. Below 8.4 there is no `SSH_ASKPASS_REQUIRE`, ssh prompts on its terminal
and ignores any helper, and tram says so rather than pretending.

The same mechanism remembers a key's passphrase. If you own the far end, a key
installed with `tram key push` is still the better answer than a remembered
password.

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

A host Ansible reaches some other way, say `ansible_connection=local`, is
skipped and named. ssh cannot be pointed at it, so importing it would only
create a stanza that fails.

Nothing is written until you say so. `--dry-run` shows the plan, an existing
host is left alone unless you pass `--overwrite`, and re-importing the same file
reports that everything already matches. In the interface, `I` does the same
thing: type a path, read the preview, press `w` to write it.

## Where things live

```
~/.ssh/config                 the source of truth. tram reads and writes it.
~/.ssh/config.d/tram.conf     tram's own file, after `tram init`
~/.ssh/*.tram.bak             a snapshot taken before each write

~/.config/tram/               (or %APPDATA%\tram\)
├── accounts.json             identities, and which host is linked to which
├── snippets.json             saved commands
├── history.json              last connected, and pinned hosts
└── config.toml               tram's own options

OS keyring                    passwords and key passphrases
```

Delete `~/.config/tram/` entirely and `tram ls` still lists every host and
`tram web1` still connects. You lose Recent, Favorites, snippets and account
links. Groups and descriptions survive, because they are kept in `ssh_config`
as comments.

After `tram init`, hosts declared outside `config.d/tram.conf` are read-only:
still listed, still connectable, still usable with `exec`, but writing to one
needs `--force`.

## The interface

Two screens.

**List.** `enter` connects, `W` opens a new window, `f` opens sftp, `space`
marks hosts, `/` searches (a query starting with `#` matches groups),
`a`/`e`/`c`/`d` add, edit, clone and delete, `E` edits everything marked, `A`
links to an account, `p`/`D`/`x`/`r` run ping, doctor, a command and a snippet,
`I` imports an inventory, `*` pins, `tab` shows detail, `?` lists the keys.

**Results.** One collapsible block per host, `space` expands, `enter` connects
to whichever host is selected. An import preview uses the same screen, where
`w` writes the hosts and `esc` throws the plan away.

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
