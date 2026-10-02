# Changelog fragments

Each work package records its user-visible changes in its own file here,
`changelog.d/<package>.md`, so that packages developed in parallel never edit
the same lines. The release integrator merges the fragments into
`CHANGELOG.md` and then deletes them.

A fragment is a plain Markdown bullet list, written for someone who uses the
provider: what changed, and what they must do about it, if anything. Mark a
breaking change with **Breaking:** at the start of its bullet. Leave out
internal refactoring and test-only changes.
