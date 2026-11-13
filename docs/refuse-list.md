# Refuse list

Paths Cubit will not read, whatever the config says. A refused path is skipped and counted. It never stops a scan.

## Matching

- **Whole components, case-insensitive.** A path is refused when its resolved form equals an entry or lies below it, compared one path component at a time and without regard to case. `~/.ssh` refuses `~/.SSH/id_x` and `~/.ssh/id_x`, but not `~/.sshfs`. Case folding applies on every platform: APFS is case-insensitive by default, and over-refusing on linux is harmless.
- **Three kinds of entry.**
  - `~/...` is below the home directory of the user running Cubit.
  - `/...` is an absolute prefix.
  - `**/...` matches at any depth under a root. It exists because a root outside `~` (for example a scan run as another user, or a copied home) would otherwise reach a `.ssh` that no `~/` entry names.
- The check runs on **every directory entry during the walk**, not only at the roots. A root at `~` is legal and the walk will not descend into `~/.ssh`.
- **Refused paths are never opened or statted.** The walker opens directories and takes sizes from `fstatat`. Refusal stops it descending into a directory or reading an entry's metadata at all, so a refused name cannot reach a report or a byte total.
- The list applies on linux and darwin alike. An entry for a path that does not exist on the host is harmless.

## Built-in entries

The first fifteen entries below are the spec's list. The rest were added at C0. Entries only ever get added; no config can remove one.

```refuse
~/.ssh
~/.gnupg
~/.aws
~/.kube
~/.config/gh
~/.netrc
~/.cargo/credentials*
~/Library/Keychains
~/Library/CloudStorage
~/Library/Mobile Documents
/etc/shadow
/etc/ssh
/proc
/sys
/dev
~/.cargo/config
~/.cargo/config.toml
~/.docker
~/.password-store
~/.git-credentials
~/.config/gcloud
~/.config/op
~/.local/share/keyrings
~/Library/Safari
~/Library/Application Support/Google/Chrome
~/Library/Application Support/Firefox
~/.mozilla
~/.config/google-chrome
~/.config/chromium
/etc/NetworkManager/system-connections
/run/secrets
**/.ssh
**/.gnupg
**/.aws
**/.kube
**/.netrc
**/.docker
**/.password-store
**/.config/gh
**/.cargo/credentials*
**/Library/Keychains
```

`credentials*` is a glob on the final component only, so it covers `credentials` and `credentials.toml`. `~/.cargo/config*` is listed because Cargo config can carry a registry token; Cubit's `cargo-home` mode only measures the fixed cache areas, but a refusal costs nothing.

## Configured entries

- `refuse.vault`: a vault prefix. It is refused like the entries above and reported by alias only, never by path.
- `refuse.extra`: up to 64 more prefixes, home-relative or absolute.

## What happens on a hit

| Where the hit is | Result |
|---|---|
| A configured root, or a discovery root, **inside** a refused prefix | `cubit validate` error (exit 4). `scan` and `serve` will not start with it. |
| A refused prefix **inside** a root | Legal. The subtree is skipped and counted as `cubit_skipped_entries{reason="refused"}`. `validate` prints an info line naming the root alias and the prefix class (never the path). A refusal does not make the root partial. |

## Containment and symlinks

The walk opens each root with `os.Root`, so a path that would leave the root fails at the syscall instead of being caught by a check that could race. `EvalSymlinks` and `Rel` run only in `validate`, to give readable errors. The security property under fuzz test (`FuzzContain`) is that no candidate path ever resolves outside its root.

Symlinks are never followed during the walk. One inside a tree is measured as link bytes and is not classified: it is neither an escape nor a refusal, because Cubit never resolves it. Cubit resolves a symlink only where it must: a configured root, or a discovered `target/`. If that one leaves the allowlisted root, or loops, it is skipped as `symlink_escape` or `symlink_loop`. `os.Root` does not decide refusal; the list does. What keeps the list sufficient is that the walker never opens a non-directory.

## Reporting

Reports, metrics and warnings name the root alias, the closed reason and a count. They never include a path, so a refused file name cannot leak through Cubit's own output.
