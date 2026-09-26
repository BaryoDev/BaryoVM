# AGENTS.md

Read [CLAUDE.md](CLAUDE.md) first. It holds the rules for this repository, for
people and for every agent, not only Claude.

## How work is done here

This repository follows the
[lean agent method](https://github.com/arnelirobles/lean-agent-method).

- Search open issues before filing. If one covers the area, add to its Covers
  list instead.
- One ticket is one agent pass, filed with the Agent-ready template: Goal,
  Where, Covers, Done when, Risks, Constraints, Out of scope.
- Scripts, not instructions: run all four CI checks under Commands in
  CLAUDE.md, not the one you have in mind. Anything reasoned through twice
  becomes a script.
- A bug fix ships with a test that failed before the fix.
- Every change gets an adversarial review by a separate agent, and findings go
  back to the agent that wrote the change.
- After a batch merges, run the retro and propose method changes with evidence.
