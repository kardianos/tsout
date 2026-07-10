# outfile patch for typescript-go

Durable patch applied on top of [microsoft/typescript-go](https://github.com/microsoft/typescript-go) `main`, published as [kardianos/tsout](https://github.com/kardianos/tsout).

| Branch | Role | Push |
|--------|------|------|
| `main` | Upstream HEAD + **one** commit (shipped tree) | `--force-with-lease` each sync |
| `outfile-patch` (this branch) | Patch artifact only | Normal commits, **never force** |

## Files

- `outfile.patch` — full `git diff` of published `main` vs upstream `main`
- `UPSTREAM.txt` — last synced upstream SHA / timestamps
- `sync-main.sh` — rebuild and push workflow

## Sync after upstream moves

From a full clone with remotes `origin` (microsoft) and `kardianos` (tsout):

```bash
# Prefer running from main after checkout of the script:
git fetch kardianos outfile-patch
git show kardianos/outfile-patch:sync-main.sh > /tmp/sync-main.sh
bash /tmp/sync-main.sh
```

Or, with this branch checked out in a worktree that still has the full object database and remotes:

```bash
./sync-main.sh
```

The script will:

1. Apply `outfile.patch` onto latest `origin/main`
2. Create a single commit on `main` and push with `--force-with-lease`
3. Tag `outfile/<upstream-short-sha>`
4. Regenerate `outfile.patch` / `UPSTREAM.txt` and push a **new** commit on this branch

## Recover `main` without the script

```bash
git fetch origin main
git fetch kardianos outfile-patch
git switch -C main origin/main
git apply --index <(git show kardianos/outfile-patch:outfile.patch)
git commit -m "internal/compiler: support outfile and cross file namespaces"
git push kardianos main --force-with-lease
```

## Why force-push `main`?

Upstream moves; “exactly one commit on top” always rewrites that tip’s parent.
`main` is rebuildable. **This branch is the seatbelt** — do not force-push it.
