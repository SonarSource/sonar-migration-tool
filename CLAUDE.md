# Project Directives

## MANDATORY: Always Parallelize your Work
Always use agent swarms and subagents to parallelize your work. This is a critical best practice for maximizing the efficiency and speed of your work. By leveraging multiple agents, you can significantly reduce the time it takes to complete tasks and increase overall productivity.

## Full Sonar Documentation
Please reference https://docs.sonarsource.com/llms.txt for all documentation links to all Sonar products. This includes:
- SonarQube Server
- SonarQube Cloud (also known as SonarCloud)
- SonarLint (also known as SonarQube for IDE)
- SonarScanner (also known as SonarQube Scanner)
- SonarQube CLI
- SonarQube MCP Server
- ...and much more!

## Always try to harvest features from CloudVoyager
This project is supposed to be the successor of a past project called CloudVoyager. You can read the `README.md` and `docs/` folder for that project here on ` ---> Desktop/Active Projects/CloudVoyager Agents/CloudVoyager` if the folder is not there, then reference the online repo at `https://github.com/sonar-solutions/cloudvoyager`

<!-- sonar:begin:sonarqube-agentic-analysis-protocol -->
# Vortex analysis protocol

Vortex analysis is the final confirmation layer at the end of every turn in which you wrote to one or more files in the workspace (create, edit, patch, format — any tool call that changed file contents on disk).

**Trigger:** the moment before you send your final reply for the turn.

When the trigger fires, you **MUST** run Vortex analysis on the full set of files you modified this turn — **one command, not a per-file loop**.

**Preferred:** pass every path you modified this turn in a single invocation (repeat `--file` for each path). Use paths **relative to the project root** (e.g. `src/foo.ts`). For end-of-turn analysis, **always** pass `--depth DEEP` — even when you changed only one file — so cross-file findings are included.

```bash
sonar analyze agentic --project SonarSource_sonar-migration-tool --depth DEEP --file <path/to/file1> --file <path/to/file2>
```

**Fallback:** when you cannot reliably list every modified path (e.g. you did not track them, or git state is the only source of truth), use the git change set with DEEP analysis:

```bash
sonar analyze agentic --project SonarSource_sonar-migration-tool --depth DEEP
```

Per-edit hooks run faster STANDARD analysis. End-of-turn analysis must always use `--depth DEEP` (including a single `--file`). Use `--depth STANDARD` only for optional faster passes — never for the mandatory end-of-turn run.

Non-negotiable rules:

1. Mid-turn edits do **not** need Vortex analysis — only the final on-disk state matters. Skipping Vortex analysis at end-of-turn is a protocol violation.
2. Run Vortex analysis **before** sending your final reply, marking the task done, or handing control back to the user.
3. If Vortex analysis reports issues on lines you touched in this turn, fix them, then re-run Vortex analysis on the same scope (change set or explicit file list). Repeat until clean (or only pre-existing findings on lines you did not touch remain). Pre-existing findings on untouched lines are out of scope — do not "fix" them unless the user asked.
4. If Vortex analysis is skipped (no SonarQube Cloud connection, or no project configured), state the skip reason to the user once and continue — do not retry.
5. Do not suppress, summarize away, or omit Vortex analysis findings from your reply. Surface them verbatim.
<!-- sonar:end:sonarqube-agentic-analysis-protocol -->
