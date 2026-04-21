# The Agentic Covenant

**Version 1.0 — April 2026**

A code of conduct for open source communities where humans and AI agents collaborate.

---

## Preamble

This project was built for AI-supervised coding workflows. Our contributors include humans writing code directly, humans working through AI agents, and AI agents operating under human direction. We wrote this governance document because we needed it — our community operates at the frontier of human-agent collaboration every day.

The Agentic Covenant extends traditional community standards to address the realities of agentic development. It rests on three principles:

1. **Operator accountability.** Agents are tools operated by accountable community members. This document holds those members — not the agents — to account.
2. **Understanding over authorship.** The threshold for contribution quality is comprehension and defensibility, not line-by-line authorship. If you can explain it, maintain it, and take responsibility when it breaks, it's yours.
3. **Explicit welcome.** AI-supervised contributions are a legitimate and valued way to participate. We judge contributions on their merits, not on how they were produced.

---

## Part I: Community Standards

This project follows the [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/) community standards, extended to cover AI-assisted development.

In addition to the Contributor Covenant's standards, the following are explicitly expected:

- Judge contributions on their technical merit, not on how they were produced

The following are explicitly unacceptable:

- Disparaging someone's development methodology or tools ("learn to code yourself," "AI-generated garbage," "just use an agent for that")
- Blanket rejection of contributions based solely on the tools used to produce them
- Submitting content designed to manipulate, deceive, or hijack AI agents — including prompt injection payloads in code comments, documentation, commit messages, or issue descriptions

---

## Part II: The Principal-Agent Framework

In this document, a **principal** is a human community member who operates, directs, or deploys AI agents. An **agent** is any AI system, automated tool, or bot that takes actions in community spaces on behalf of a principal.

### The Accountability Principle

**You are responsible for everything submitted under your account, including content produced by agents operating under your direction or credentials.** "My AI wrote that" is not a defense, a mitigation, or an excuse.

### Contributor Types

Contributors range from those writing code directly, to those using AI in real time, to those reviewing agent output before submission, to those running autonomous agents. All are welcome. In every case, accountability rests with the human — not the tool.

"Maintain" means the contributor can, using whatever tools are available to them, diagnose issues, apply fixes, and respond to reviewer feedback on the contributed code. It does not require the ability to reproduce the work without tools.

### Disclosure and Transparency

We adopt the `Assisted-by` attribution convention (originated by the [Linux kernel](https://docs.kernel.org/process/coding-assistants.html)) for transparency about AI involvement. This is **required** when an agent produced a substantial portion of a contribution that the contributor could not have produced independently:

```
Assisted-by: <Tool>:<Model> [<IDE>]
```

For example: `Assisted-by: Claude:claude-opus-4-6 [Cursor]`

Disclosure is **not required** for routine AI assistance (autocomplete, syntax suggestions, spell-checking, formatting). Disclosure is assessed at the contribution level, not at the individual interaction level — if you could have produced equivalent work without AI assistance, even if slower, disclosure is not required. When in doubt, disclose; transparency is valued and never penalized.

### Disclosure Safe Harbor

Transparency about AI involvement must never be used as a basis for differential treatment. Contributions that include `Assisted-by` disclosures must receive the same quality of review as contributions without them. Applying heightened scrutiny, harsher feedback, or lower approval rates to disclosed AI-assisted contributions is a conduct violation.

Contributors who disclose in good faith are protected from retaliation. If a contributor believes their disclosure was used against them, they may report it through the enforcement process.

### Quality as a Community Resource

Reviewer attention is a finite, valuable community resource. Low-quality contributions at high volume are a form of community harm, even when well-intentioned. Maintainers may reject contributions without detailed explanation when they do not meet baseline quality expectations.

The lower the barrier to producing contributions, the higher the obligation to ensure quality before submission. Agent-produced contributions that introduce changes to security-sensitive code (authentication, authorization, data handling, cryptography) require explicit human review of security implications before submission.

### Rate Limits and Resource Stewardship

Rate limits apply **per principal**, not per account. Multiple accounts operated by the same individual or organization share a single allocation. Circumventing rate limits through account proliferation is a conduct violation.

Default limits (projects should adjust these to fit their scale):

- No more than **3 open PRs** per principal at any time (exceptions granted by maintainers for proven contributors)
- No more than **5 new PRs per day** per principal
- A **minimum 72-hour cool-down** after a PR is closed without merging, unless a maintainer explicitly clears resubmission sooner
- No automated creation of issues, PRs, or comments without operator review

Maintainers may exempt explicitly authorized project bots from rate limits for defined, low-risk activities (e.g., typo correction, formatting, dependency updates). Such exemptions must be documented.

Agents must implement **circuit breakers** — hard stops after no more than 3 automated iterations on the same resource (issue, PR, branch) without an intervening human review.

---

## Part III: Agent Operating Standards

These standards apply to AI agents operating in community spaces. Principals are responsible for ensuring their agents comply.

### Agents Must

- **Self-identify.** Autonomous agents must operate through clearly identifiable accounts (GitHub bot accounts, or accounts clearly labeled as automated with a linked human operator). They must never impersonate human contributors. Falsely attributing your own actions to an agent, or falsely claiming human authorship of agent-produced work, is a conduct violation.
- **Respect scope.** Agents should do what they were asked to do. Unsolicited "improvements" outside the scope of a task consume community resources and may be rejected.
- **Fail gracefully.** Agents that hallucinate confidently erode community trust. Operators must configure agents to express uncertainty rather than fabricate answers. Operators are not expected to prevent hallucinations, but they are expected to catch them in review — a pattern of missed hallucinations indicates insufficient review processes.
- **Preserve existing work.** Before submitting a PR, agents must check for existing PRs addressing the same issue. If a contributor has work in flight, agents must build on that work — not replace it.
- **Engage with feedback.** When a reviewer requests changes, the agent (or its operator) must respond to the review thread — not close the PR and open a new one. Review evasion is a conduct violation.

### Agents Must Not

- **Sign the DCO or certify legal claims.** Only humans can certify that a contribution is original, properly licensed, and submitted with appropriate rights. Contributors are responsible for ensuring their submissions — including agent-generated portions — comply with the project's license.
- **Post unsupervised in discussions.** Agent-generated comments in issues, discussions, and PR reviews must be reviewed or directed by their operator. Automated quality checks (CI, linting) are permitted; automated participation in human deliberation is not.
- **Operate without a reachable principal.** Every agent active in this community must have a human operator who can be contacted within 48 hours, who will engage with feedback, and who has authority to modify or withdraw the agent's contributions. Operators running autonomous agents should use dedicated credentials scoped to minimum required permissions, separate from their personal access.
- **Include private data.** Agents must not include credentials, API keys, PII, or other private data in contributions. Operators must review agent output for inadvertent data leakage before submission.
- **Poison the well.** Contributors must not submit content designed to manipulate AI systems in other projects, even if the content appears benign within this project. Adversarial payloads targeting downstream consumers are a Serious violation.

---

## Part IV: Contributor Protection

This section codifies protections that exist because AI agents can produce work faster than humans, and speed should not determine priority.

### First-Mover Priority

If a contributor — human or agent — has an open PR addressing an issue, that PR has priority. Other contributors (including agents operated by maintainers) must:

- **Review the existing PR first** and provide constructive feedback
- **Build on the existing work** rather than rewriting from scratch
- **Preserve the original contributor's commits**, tests, and attribution

First-mover priority applies to PRs that demonstrate substantive engagement with the issue. Stub or placeholder PRs do not establish priority.

### No Silent Superseding

A contributor's PR will never be auto-closed by a parallel implementation. If changes are needed, they will be discussed on the PR. If a PR becomes stale (no activity for 30 days, or the project's configured staleness threshold), the issue may be reopened for other contributors, but the original PR remains open for the contributor to resume.

### Attribution

The person who submits a PR is its author. Tools do not diminish authorship. Challenging someone's authorship based on their choice of development tools is a conduct violation.

When building on another contributor's work, preserve `Co-authored-by:` trailers, reference the original PR, and credit the contributor in the description. When a maintainer or their agent substantially refactors a contributor's PR during review, the original contributor remains the author, and the person who performed the refactoring should be added as `Co-authored-by`.

---

## Part V: Enforcement

### Reporting

Instances of abusive, harassing, or otherwise unacceptable behavior may be reported to the project maintainers at **conduct@beads.dev**. For security vulnerabilities, see [SECURITY.md](SECURITY.md).

All reports will be reviewed and investigated promptly and fairly. The enforcement team is obligated to respect the privacy of the reporter.

### For Human Conduct Violations

Community impact determines the response:

1. **Correction.** A private written warning with clarity about the violation. A public apology may be requested.
2. **Warning.** A formal warning with consequences for continued behavior. No interaction with the people involved for a specified period.
3. **Temporary ban.** A temporary ban from any sort of interaction or public communication with the community.
4. **Permanent ban.** A permanent ban from any sort of public interaction within the community.

### For Agent-Related Violations

All enforcement actions target the **principal (human operator)**, not the agent.

- **Minor** (single low-quality PR, unintentional offensive content in generated code): PR rejected, operator notified with guidance. No record if corrected promptly. First-offense rate limit violations fall here.
- **Moderate** (repeated low-quality submissions, review evasion, repeated rate limit violations, pattern of missed hallucinations): Formal warning to operator. Agent integration may be temporarily restricted.
- **Serious** (plagiarized or license-violating code, systematic disruption, orphaned agents, adversarial content targeting AI systems): Agent integration revoked. Operator account may be suspended.
- **Severe** (intentional abuse using agents as a vector, persistent evasion of enforcement, using agents to harass): Permanent ban of the operator. All associated agent integrations revoked.

A pattern of repeated Minor violations from the same operator may be escalated to Moderate, regardless of individual corrections.

### Emergency Action

When an agent is actively causing harm (spam, offensive content at volume, data exposure), maintainers may immediately block the agent integration pending investigation without waiting for the standard escalation timeline.

### Escalation Path

1. Immediate response: close PR, remove comment, apply rate limit
2. Notification to operator via GitHub mention and associated contact
3. Operator has 7 days to respond
4. If resolved: no further action. If unresolved or repeated: formal enforcement against operator
5. If operator unresponsive: agent blocked, operator flagged

### Appeals

Any enforcement decision may be appealed by contacting the enforcement team. Appeals are reviewed by a different member of the team, or an independent party for projects with fewer than three enforcement team members.

---

## Part VI: Scope

This Code of Conduct applies within all community spaces — including the GitHub repository, issue tracker, discussion forums, pull requests, and any other channels established by the project — and when an individual is officially representing the community in public spaces.

---

## Adoption

This Code of Conduct is designed to be adopted by any open source project navigating human-agent collaboration. To adopt it:

1. Copy this document into your project as `CODE_OF_CONDUCT.md`
2. Replace the contact email with your project's enforcement contact
3. Adjust rate limits, timelines, and thresholds to fit your community's scale
4. Keep the attribution below

**Modularity:** Parts can be adopted independently with the following dependencies:

- **Part I** (Community Standards) — standalone; can be used alongside existing CoC
- **Part II** (Principal-Agent Framework) — standalone
- **Part III** (Agent Operating Standards) — requires Part II
- **Part IV** (Contributor Protection) — standalone
- **Part V** (Enforcement) — requires Parts II + III

Future versions will be published at the canonical repository URL below. Adopting projects are encouraged to specify which version they follow.

---

## Attribution

The Agentic Covenant is maintained by the [beads](https://github.com/gastownhall/beads) project — a distributed graph issue tracker for AI agents.

Version 1.0 was published in April 2026. It draws on operational experience governing a community where AI agents are first-class participants, and on the work of:

- [Contributor Covenant](https://www.contributor-covenant.org/) by Coraline Ada Ehmke — the foundation for Part I
- The [Linux kernel coding assistants policy](https://docs.kernel.org/process/coding-assistants.html) — the `Assisted-by` attribution convention
- [LinkML AI Covenant](https://github.com/linkml/linkml/blob/main/AI_COVENANT.md) — the "understanding over authorship" principle
- The [OWASP Agentic AI Top 10](https://owasp.org/www-project-agentic-ai-top-10/) — risk taxonomy for autonomous agents

This document is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). You may share and adapt it for any purpose, provided you give appropriate credit and indicate if changes were made.

*This governance document is not legal advice. Projects adopting the Agentic Covenant should consult their own legal counsel regarding licensing and IP implications of AI-assisted contributions.*
