# Commit and release workflow

All commit messages and release notes should be written in English.
These scripts run on the maintainer's machine, not inside the agent sandbox.
They require Bash, Git, Go 1.25+, and a working Git editor; releases also use curl
for the catalog refresh.

## Commit and push

```bash
./push
./push "feat(cli): add an editable release workflow"
```

`push` stages all changes from the repository root, opens a prefilled message
with the staged diff summary, validates typed entries, commits, then pushes only
if the commit succeeds. By default the editor contains both common entries:

```text
feat: <what was added>

fix: <what was fixed>
```

Complete either or both in English. Unchanged default placeholders and empty
typed entries are automatically removed before committing; leaving everything
unchanged cancels instead of committing placeholders. You can delete unused
entries yourself or add more entries, including several of the same type.
The optional argument prefills your own message but still opens the editor.

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)
syntax for each entry. This project's release parser also supports typed entries
in the commit body, so one commit can contribute to multiple changelog sections:

```text
feat(cli): add editable release notes

fix(cli): prevent publishing before approval

fix(config): preserve saved preferences
```

The first completed entry is the Git subject; the others remain in the body.
The release reads **all** unindented `type(scope): summary` lines, not just the
subject. Separate entries with blank lines; `push` inserts missing separators.
Plain body prose and fenced code examples are not separate changelog entries.
Indent a typed example or put it inside a fenced code block if it is only
illustrative. `refactor` and `perf` entries are also supported.

Other supported types: `docs`, `style`, `test`, `build`, `ci`, `chore`, `revert`.
The scope is optional. For breaking changes, use `!` before the colon and/or a
`BREAKING CHANGE: explanation` footer **after the relevant entry and before the
next typed entry**. The footer applies only to that entry, not every entry in the
commit. Deleting all non-comment text cancels;
changes remain staged. An editor or commit failure never triggers a push.
Changes to HEAD or the staged diff during editing require restarting.

The editor follows Git's normal selection (`GIT_EDITOR`, `core.editor`,
`VISUAL`, `EDITOR`, then Git's default). GUI editors must wait until the file is
closed, for example `GIT_EDITOR='code --wait' ./push`.

## Prepare and approve a release

```bash
./release.sh 1.0.1
```

Use a SemVer version without a leading `v`. The script requires `master` and a
clean working tree, and checks local and remote tags before preparing notes.
The same version must not already appear in `CHANGELOG.md`. The original
`1.0.0` entry predates this workflow: if it is only a future-release placeholder,
remove it in a normal commit before preparing `1.0.0`; do not remove real history.

1. Read commits since the nearest reachable `v<version>` tag (including
   lightweight tags). With no previous release tag, read all history.
2. Draft **only the new release section** in a temporary Markdown file, using
   Keep a Changelog categories and a dated `## [version] - YYYY-MM-DD` heading.
   `feat` maps to Added; `fix` to Fixed; `refactor`, `perf`, and `revert` to
   Changed. All typed entries in each commit are processed independently,
   including body entries. Breaking changes always appear under Changed.
   Maintenance types
   are omitted unless breaking. Move entries into Deprecated, Removed or
   Security as appropriate during review. Merge commits are skipped.
3. Open that short file in the Git editor. Rewrite, combine or delete entries
   into user-facing English; the complete historical changelog is never opened.
   Unstructured legacy messages are marked `TODO:` with their commit hash and
   **must be rewritten or removed**. An empty draft also requires a summary.
4. Show the edited section and ask `Approve these notes and release v…?`.
   Answer `e` to edit again, `y` to approve, or anything else to cancel.
   Missing input defaults to cancellation. Invalid sections cannot be approved.
5. Only after approval, refresh the Codex fallback and bundled catalog, update
   `version`, and insert the approved section before historical releases.
   Existing history and any Unreleased section are retained.
6. Stage only release artifacts, commit `chore(release): v<version>`, create an
   annotated tag, and atomically push master and the tag together.

Cancellation before approval changes no release files and creates no commit,
tag or push. Changes to the working tree or HEAD during review abort the
release. A later refresh, hook or push failure can leave local release artifacts;
inspect them before retrying. If the atomic push fails, no remote release is
published, but the local release commit and tag remain for recovery.

GitHub Actions extracts the exact approved version section from the committed
`CHANGELOG.md` and passes it to GoReleaser with `--release-notes`. It does not
regenerate or publish the full historical changelog. Tagging manually without a
valid, dated changelog section makes release CI fail rather than publish
unreviewed notes.

## Verification

```bash
bash -n push release.sh scripts/workflow-common.sh
go test -buildvcs=false ./internal/releasenotes ./cmd/release-notes
```

The workflow tests use mocked Git, editor and refresh commands: they do not
commit, tag, push, access the network or modify the real repository.
