# Domain Docs

This repository uses a single-context domain-documentation layout.

## Before exploring

Read these files when they exist:

- `CONTEXT.md` at the repository root.
- Relevant ADRs under `docs/adr/`.

Missing files are not errors. `/domain-modeling` creates them lazily when terminology or architectural decisions are resolved.

## Vocabulary

Use the canonical terms defined in `CONTEXT.md` in specifications, issue titles, tests, interfaces, and documentation. Avoid introducing synonyms that conflict with the glossary.

## Architectural decisions

If proposed work conflicts with an existing ADR, surface the conflict explicitly instead of silently overriding it.
