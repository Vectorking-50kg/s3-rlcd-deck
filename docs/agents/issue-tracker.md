# Issue tracker: GitHub

Issues and specs for this repository live as GitHub Issues. Use the `gh` CLI for all operations and infer the repository from `git remote -v`.

## Conventions

- Create: `gh issue create --title "..." --body "..."`
- Read: `gh issue view <number> --comments`
- List: `gh issue list --state open`
- Comment: `gh issue comment <number> --body "..."`
- Label: `gh issue edit <number> --add-label "..."`
- Close: `gh issue close <number> --comment "..."`
- Pull requests are not treated as a triage request surface.

## Skill operations

- “Publish to the issue tracker” means creating a GitHub Issue.
- “Fetch the relevant ticket” means reading the corresponding Issue and comments.
- Specs and implementation tickets should link to one another.
- Blocking relationships should use GitHub native issue dependencies when available.
- If native dependencies are unavailable, add `Blocked by: #<number>` to the ticket body.
- A ticket is ready only when all its blockers are closed.
