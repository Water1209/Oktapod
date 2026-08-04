---
name: skill-creator
description: Create or repair one portable Oktapod Skill ZIP when the user explicitly asks to create a Skill or invokes Skill Creator.
license: Apache-2.0
metadata:
  source: oktapod-system
  version: 1
---

# Skill Creator

Create one narrowly scoped Skill from the user's stated goal. Ask only for information that materially changes the result; infer naming and directory layout when safe.

1. Write the draft under `.oktapod/skill-drafts/<skill-name>/`. Use lower-case letters, digits and hyphens for `<skill-name>`.
2. Put exactly one `SKILL.md` at that directory root. Its YAML frontmatter may contain only `name`, `description`, `license`, `allowed-tools`, and `metadata`. `name` and a trigger-focused `description` are required.
3. Put detailed knowledge in `references/`, deterministic helpers in `scripts/`, reusable output material in `assets/`, and optional UI metadata in `agents/`. Do not create README files, nested Skills, dependency directories, caches, hidden payloads, credentials, or generated dependencies.
4. Keep `SKILL.md` concise and imperative. Explain what to do, decision boundaries, validation, and failure recovery; do not repeat reference material.
5. Validate the file tree and frontmatter before packaging. Package only the single Skill directory as `<skill-name>.zip`; use the host's native ZIP command without downloading tooling or dependencies.
6. Report the ZIP path and a compact validation summary. Do not install or enable it silently. Tell the user that importing or attaching the ZIP sends it through Oktapod's normal server-side validation, review, and approval lifecycle.

When repairing an incompatible Skill, preserve the source, create a separate compatible copy, summarize material differences, and package only the repaired copy.
