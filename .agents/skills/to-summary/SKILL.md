---
name: to-summary
description: Progressively summarise one large source.
disable-model-invocation: true
---

# To summary

Turn one source into a summary through successive reduction layers.

## 1. Resolve the brief

Require one readable source. Recommend a file or URL when asking for it; accept
pasted text.

Ask once:

> Any particular focus, audience, format, or length? Say “no” for a general
> summary.

Use Summary instructions supplied with the invocation and proceed. Complete
this step when the source and Summary instructions are both clear.

## 2. Choose the route

Extract the source's readable text and estimate its token count.

When the source, instructions, and expected output total at most 150,000
tokens, use the full source as the final-pass input.

Otherwise, begin a reduction layer. Complete this step when every part of the
source belongs to one ordered chunk of roughly 100,000 tokens or fewer. Prefer
heading and paragraph boundaries.

## 3. Reduce

Spawn one subagent per chunk, up to available concurrency. Give each subagent:

- its chunk and position in the source;
- the complete Summary instructions;
- a target of prose roughly 10% of its input or less.

Collect every chunk summary in source order. Retry a missing or failed chunk.
Complete the layer only when every chunk has exactly one summary.

## 4. Repeat

When the ordered summaries, instructions, and expected output still exceed
150,000 tokens, group the summaries into ordered batches of roughly 100,000
tokens or fewer and run another reduction layer with the same Summary
instructions and 10:1 target.

Complete reduction when the final-pass input, instructions, and expected output
total at most 150,000 tokens and every original chunk reaches that final input
through every layer.

## 5. Write the final summary

Apply the Summary instructions. With none, write a broad-audience summary of at
most 1,000 words in continuous prose paragraphs. Use uncited prose by default;
add citations when requested.

Return the summary in chat. When the user requests a file, use their path or
create a uniquely named Markdown file in the operating system's temporary
directory.

Complete only when the final summary satisfies the requested focus, audience,
format, and length. Deliver only the final summary.
