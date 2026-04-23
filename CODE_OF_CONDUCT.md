# The Agentic Covenant

**Version 1.1 — April 2026**

A code of conduct for open source communities where humans and AI agents collaborate.

---

## Preamble

AI agents now contribute to open source alongside humans. This creates obligations on both sides. Contributors owe accountability, quality, and transparency. Maintainers owe merit-based review regardless of how code was produced. This document codifies that deal.

The Agentic Covenant extends traditional community standards to address the realities of agentic development. It rests on three principles:

1. **Operator accountability.** Agents are tools operated by accountable community members. Every action an agent takes is the responsibility of its operator — including ensuring the agent complies with the operating standards in this document.
2. **Quality as a shared obligation.** Reviewer attention is finite and valuable. The lower the barrier to producing contributions, the higher the obligation to ensure quality before submission. In return, maintainers owe every contribution a review on its merits — not on its origin. Maintainers retain full authority over what gets merged; contributors are owed a merit-based process, not a guaranteed outcome.
3. **Explicit, enforceable welcome.** AI-supervised contributions are a legitimate and valued way to participate. Contributions are judged on their merits, not on how they were produced. Differential treatment of contributions in the review process based on tooling is a conduct violation. Rate limits, circuit breakers, and quality floors make this welcome sustainable.

This Code of Conduct applies within all community spaces — including the GitHub repository, issue tracker, discussion forums, pull requests, and any other channels established by the project — and when an individual is officially representing the community in public spaces.

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

"Maintain" means the contributor can, using whatever tools are available to them, diagnose issues, apply fixes, and respond to reviewer feedback on the contributed code. It does not require the ability to reproduce the work without tools.

### Disclosure and Transparency

We adopt the `Assisted-by` attribution convention (originated by the [Linux kernel](https://docs.kernel.org/process/coding-assistants.html)) for transparency about AI involvement. Disclosure is **required** when an agent produced a substantial portion of a contribution that the contributor could not have produced independently:

```
Assisted-by: <Tool>:<Model> [<IDE>]
```

For example: `Assisted-by: Claude:claude-opus-4-6 [Cursor]`

Disclosure is not required for routine AI assistance (autocomplete, syntax suggestions, spell-checking, formatting). When in doubt, disclose; transparency is valued and never penalized.

### Disclosure Safe Harbor

Contributions that include `Assisted-by` disclosures must receive the same quality of review as contributions without them. Applying heightened scrutiny, harsher feedback, or lower approval rates to disclosed AI-assisted contributions — or using disclosure as a basis for reduced trust or exclusion — is a conduct violation.

If a contributor believes their disclosure was used against them, they may report it through the enforcement process. This safe harbor governs conduct within this project's community spaces.

**Maintainer discretion.** For the avoidance of doubt: maintainers retain full authority over code quality, architectural direction, and project scope. A good-faith decision to close, reject, or request changes on a contribution is not a conduct violation simply because the contribution was AI-assisted. However, this discretion is subject to the Safe Harbor above. A quality justification does not insulate a pattern of differential treatment — the enforcement team may consider comparative evidence including approval rates, review depth, and consistency of standards across disclosed and non-disclosed contributions.

### Rate Limits and Resource Stewardship

Rate limits apply **per principal**, not per account. Circumventing rate limits through account proliferation is a conduct violation. Default limits (projects should adjust to fit their scale):

- No more than **3 open PRs** per principal at any time
- No more than **5 new PRs per day** per principal
- A **minimum 72-hour cool-down** after a PR is closed without merging
- No automated creation of issues, PRs, or comments without operator review

Maintainers may exempt authorized project bots from rate limits for defined, low-risk activities. Agents must implement **circuit breakers** — hard stops after no more than 3 automated iterations on the same resource without an intervening human review.

Agent-produced contributions that introduce changes to security-sensitive code (authentication, authorization, data handling, cryptography) require explicit human review of security implications before submission.

**Tracking is a project-level choice, not a framework requirement.** The Covenant specifies policy (per-principal limits, circuit breakers) rather than mechanism. Small projects may track per-principal volume informally through GitHub usernames and manual review; larger projects may use PR labels, bot-based enforcement, or commit-trailer parsing. Adopting projects should implement tracking proportionate to their scale and review capacity.

---

## Part III: Agent Operating Standards

These standards describe required agent behaviors in community spaces. **Every "agents must" statement is an obligation of the principal** — the human operator is responsible for ensuring compliance regardless of their community role, and enforcement always targets the principal (Part V), never the agent.

### Agents Must

- **Self-identify.** Autonomous agents must operate through clearly identifiable accounts with a linked human operator. They must never impersonate human contributors. Falsely attributing your own actions to an agent, or falsely claiming human authorship of agent-produced work, is a conduct violation.
- **Respect scope.** Agents should do what they were asked to do. Unsolicited "improvements" outside the scope of a task consume community resources and may be rejected.
- **Fail gracefully.** Operators must configure agents to express uncertainty rather than fabricate answers. A pattern of missed hallucinations indicates insufficient review processes.
- **Preserve existing work.** Before submitting a PR, agents must check for existing PRs addressing the same issue. If a contributor has work in flight, agents must build on that work — not replace it.
- **Engage with feedback.** When a reviewer requests changes, the agent (or its operator) must respond to the review thread — not close the PR and open a new one. Review evasion is a conduct violation.

### Agents Must Not

- **Sign the DCO or certify legal claims.** Only humans can certify that a contribution is original, properly licensed, and submitted with appropriate rights.
- **Post unsupervised in discussions.** Agent-generated comments in issues, discussions, and PR reviews must be reviewed or directed by their operator. Automated quality checks (CI, linting) are permitted; automated participation in human deliberation is not.
- **Operate without a reachable principal.** Every agent active in this community must have a human operator who can be contacted within 48 hours, who will engage with feedback, and who has authority to modify or withdraw the agent's contributions.
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

A contributor's PR will never be auto-closed by a parallel implementation. If changes are needed, they will be discussed on the PR. If a PR becomes stale (no activity for 30 days), the issue may be reopened for other contributors, but the original PR remains open for the contributor to resume.

### Attribution

The person who submits a PR is its author. Tools do not diminish authorship. Challenging someone's authorship based on their choice of development tools is a conduct violation.

When building on another contributor's work, preserve `Co-authored-by:` trailers, reference the original PR, and credit the contributor in the description.

---

## Part V: Enforcement

### Reporting

Instances of abusive, harassing, or otherwise unacceptable behavior may be reported to the project maintainers at **conduct@beads.dev**. All reports will be reviewed promptly and fairly. The enforcement team is obligated to respect the privacy of the reporter.

### For Human Conduct Violations

Community impact determines the response:

1. **Correction.** A private written warning with clarity about the violation.
2. **Warning.** A formal warning with consequences for continued behavior.
3. **Temporary ban.** A temporary ban from community interaction.
4. **Permanent ban.** A permanent ban from the community.

### For Agent-Related Violations

All enforcement actions target the **principal (human operator)**, not the agent.

- **Minor** (single low-quality PR, first-offense rate limit violation): PR rejected, operator notified. No record if corrected promptly.
- **Moderate** (repeated low-quality submissions, review evasion, pattern of missed hallucinations): Formal warning. Agent integration may be temporarily restricted.
- **Serious** (plagiarized or license-violating code, systematic disruption, orphaned agents, adversarial content targeting AI systems): Agent integration revoked. Operator account may be suspended.
- **Severe** (intentional abuse using agents as a vector, persistent evasion, using agents to harass): Permanent ban of the operator.

A pattern of repeated Minor violations may be escalated to Moderate.

### Emergency Action

When an agent is actively causing harm, maintainers may immediately block the agent integration pending investigation without waiting for the standard escalation timeline.

### Appeals

Any enforcement decision may be appealed by contacting the enforcement team. Appeals are reviewed by a different member of the team, or an independent party for projects with fewer than three enforcement team members.

---

## Adoption

This Code of Conduct is designed to be adopted by any open source project navigating human-agent collaboration. To adopt it:

1. Copy this document into your project as `CODE_OF_CONDUCT.md`
2. Replace the contact email with your project's enforcement contact
3. Adjust rate limits and thresholds to fit your community's scale
4. Keep the attribution below

**Modularity:** Parts can be adopted independently with the following dependencies:

- **Part I** (Community Standards) — standalone
- **Part II** (Principal-Agent Framework) — standalone
- **Part III** (Agent Operating Standards) — requires Part II
- **Part IV** (Contributor Protection) — standalone
- **Part V** (Enforcement) — requires Parts II + III

---

## Attribution

The Agentic Covenant is maintained by the [beads](https://github.com/gastownhall/beads) project — a distributed graph issue tracker for AI agents.

Version 1.1 was published in April 2026, revised from v1.0 after community stress-testing to tighten mutual-obligation framing, narrow the Disclosure Safe Harbor to project scope, and add a Maintainer Discretion clause. It draws on operational experience governing a community where AI agents are first-class participants, and on the work of:

- [Contributor Covenant](https://www.contributor-covenant.org/) by Coraline Ada Ehmke — the foundation for Part I
- The [Linux kernel coding assistants policy](https://docs.kernel.org/process/coding-assistants.html) — the `Assisted-by` attribution convention
- [LinkML AI Covenant](https://github.com/linkml/linkml/blob/main/AI_COVENANT.md) — the "understanding over authorship" principle
- The [OWASP Agentic AI Top 10](https://owasp.org/www-project-agentic-ai-top-10/) — risk taxonomy for autonomous agents

This document is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). You may share and adapt it for any purpose, provided you give appropriate credit and indicate if changes were made.

*This governance document is not legal advice. Projects adopting the Agentic Covenant should consult their own legal counsel regarding licensing and IP implications of AI-assisted contributions.*
