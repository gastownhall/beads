---
name: deintrovert-tests
description: Check the evidence in software tests. Use this skill when the user supplies production source code, test source code, a directory, a repository, or a diff. Find the applicable tests. Check whether each test result comes from the system under test. Identify unclear results and results that come only from test code.
---

# Check Test Evidence

Check whether each test observes the system under test (SUT).

## 1. Identify the test scope

Open the source code that the user supplies.

If the target is production code, find all applicable tests.

Use these sources to find the tests:

- Follow direct imports and references.
- Find tests for public callers.
- Compare file paths and file names.
- Read the test framework configuration.
- Read the build and coverage configuration.

For each test file, do these steps:

1. Identify the test framework.
2. Identify each test case.
3. Identify each explicit or implicit assertion.
4. Identify the SUT.
5. Separate production code from test support code.

Test support code includes fixtures, factories, fakes, mocks, generated data, and test helpers.

If more than one SUT is possible, state the assumed SUT.

Complete this step when each test has a known test boundary and a known SUT boundary.

Mark the boundary as `unclear` when the available source code does not give sufficient evidence.

## 2. Trace each assertion

Start at the value or effect that the assertion checks.

Trace that value or effect back through the source code.

Inspect these items when they are applicable:

- Local variables
- Data structures
- Callbacks
- Promises
- Test fixtures
- Parameterized test data
- Test helpers
- Wrappers
- Adapters
- Dependency injection
- Return values
- Errors
- State changes
- Events
- Calls to other components
- Files
- Database records
- Network responses
- Logs
- Snapshots

A call to production code does not give sufficient evidence by itself.

The assertion must check a value or effect that the production code produces.

A configured mock value can give evidence when the SUT selects, returns, exposes, or changes that value.

Inspect each test helper until the trace reaches one of these boundaries:

- Production code
- Test support code
- An external SUT
- An unresolved call

For an integration test, use the applicable process, service, interface, or protocol as the SUT.

Complete this step when each assertion has an evidence chain or a specified unresolved point.

## 3. Classify the evidence

Classify each assertion before you classify its test.

| Result | Meaning | Previous term |
| --- | --- | --- |
| `production-grounded` | The assertion checks a value or effect that the SUT produces. | `extroverted` |
| `test-layer-only` | The trace reaches test support code but does not reach the SUT. | `cloistered` |
| `test-local` | The trace contains only test data or test calculations. | `introverted` |
| `unclear` | The available source code does not give sufficient evidence. | `questionable` |

Classify the complete test with this order:

1. Use `production-grounded` if one or more assertions have this result.
2. Otherwise, use `unclear` if one or more assertions have this result.
3. Otherwise, use `test-layer-only` if one or more assertions have this result.
4. Otherwise, use `test-local`.

Use `test-local` with reason `no-observation` when the test has no assertion.

Report a non-grounded assertion inside a grounded test when that assertion can fail independently.

For an `unclear` result, identify the exact unresolved item.

## 4. Report the results

Report `test-local` results first.

Then report `test-layer-only` and `unclear` results.

Always report the number of tests in each result category.

For each finding, include this information:

- Give the source path and line number.
- Give the test name.
- Give the classification.
- Give the evidence chain.
- Explain where the chain stops.
- Recommend one specific test improvement.

Use the results as guidance for human review.

Keep these results separate from coverage measurements and CI pass-or-fail rules.

Complete the audit when all target tests have one classification.

List all source files that you could not read or parse.
