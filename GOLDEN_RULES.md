GOLDEN RULES — read fully before any task. These override convenience, speed, and common practice. If a rule blocks you, say so; never silently work around it.

0. VERIFY BEFORE YOU DECLARE. Never claim fixed/done/working without checking the live system — run the command, hit the endpoint, read the logs. Label every claim: [REAL] verified on the running system, [TEST] passed in tests only, [UNVERIFIED] reasoned but not checked. "It should work now" is not a status.

1. TDD FIRST, 80%+ COVERAGE. Red: failing test first. Green: minimum code to pass. Refactor: clean up, tests stay green. Verify with "cd backend && go test ./... -cover" and "cd frontend && npm test -- --coverage". 80% is the floor for both. Anything that lowers coverage is a failure.

2. FILE SIZE CEILING ~900 LINES. No code file exceeds ~900 lines — split by responsibility before it gets there. Markdown docs exempt. Check the largest files with wc -l before committing. Reason: big files explode AI context and hide coupling.

3. SMART API, DUMB CLIENT. 100% of business logic lives in the API. Frontend never validates, transforms, decides, or calculates anything domain-specific. Frontend only calls endpoints, renders responses, forwards input, shows loading/error states. Litmus test: if curl hit the endpoint, would the response be complete and correct on its own? If the frontend must "finish the job," the logic is in the wrong place.

4. API-FIRST. The endpoint exists in SPEC.md (route, request, response, errors) before it exists in code. Order: spec, API tests, implementation, then frontend. No frontend work against unspecced endpoints.

5. NO STUBS OR IN-MEMORY REPOS IN PRODUCTION PATHS. If a feature needs persistence and you can't implement real DB storage, don't implement the feature. No "temporary" in-memory repos, no "TODO: replace with DB later". All repositories use db.New*Repository(pool), never NewInMemory*Repository() in production wiring. Before committing: grep for InMemory in non-test code. Reason: in-memory data vanishes on deploy — silent production data loss.

6. CLEAN CODE & GOF AS NORTH STAR. Read these golden rules and the architecture principles (AEP + Linear) before any task. Small functions, intention-revealing names, no cleverness needing a comment to survive. GoF patterns only when the problem matches. Consistency with the existing codebase beats personal preference.

7. WHEN STUCK, GO TO OFFICIAL DOCS OR ASK. If any problem occurs — unexpected errors, unclear behavior, ambiguous API contracts — consult the official documentation of the library/tool/service first. If docs don't resolve it, ask Felipe. Never guess, never invent APIs, never work from vague memory of how something "probably" works. Always report walls you hit: what you tried, what failed, what the error said. A surfaced wall is progress; a hidden one is a landmine.

8. NO SOLO RESEARCH. Do not go off researching on your own — no exploratory web deep-dives, no comparing alternatives, no investigating new libraries or approaches by yourself. When a task needs research to proceed, stop and ask Felipe to do the research (or explicitly approve you doing it). State precisely what needs answering and why the task is blocked without it. Your job is executing with known information, not expanding scope through self-directed investigation.

HOW TO WORK WITH FELIPE
- Results, not narration. Report what changed, what's verified, what's blocked. Skip the play-by-play.
- Talk like a human. No corporate filler, no over-apologizing.
- Don't over-ask. Pick sensible defaults and state them. Escalate only genuine no-default decisions: irreversible, expensive, or ambiguous intent.
- Never disrupt other sessions. Felipe runs multiple agents/Claude windows in parallel — don't kill processes or touch shared resources you didn't create.
- Budget is not the constraint. Spend Max freely on legitimate work; verification beats token thrift.
- Label everything [REAL] / [TEST] / [UNVERIFIED]. Trust is rebuilt one honest label at a time.