---
name: threads-post-writer
description: >
  Craft punchy, conversational Threads posts (hook + reply body) from raw content, logs, or ideas.
  Use when the user wants to share something on Threads — a trade signal, a build update, a hot take,
  or anything that needs to land as authentic and engaging rather than polished and corporate.
  Triggers on phrases like "post this to threads", "make a threads post", "write a threads message",
  "threads-ify this".
---

# Threads Post Writer

Turn raw content — logs, ideas, trade signals, build updates — into a two-part Threads post that
feels like a real person wrote it at 2pm on a Tuesday, not a marketing team.

## Platform Rules (as of 2026)

| Rule | Value |
|---|---|
| **Hook character limit** | 500 chars (aim for 150–300 for best reach) |
| **Text attachment** | Up to 10,000 chars (not always available — use reply-to-self as fallback) |
| **Tags** | ONE tag per post only. Phrase tags work (e.g. "Algorithmic Trading") |
| **Cashtags** | `$TSLA`, `$BTC` etc. are clickable and searchable |
| **Emojis** | Max 1 in hook, max 2 total across hook + body |
| **Hashtags** | Only one indexes — don't stack them |
| **Links** | Full URL counts toward character limit — use shorteners if needed |
| **Line breaks** | Preserved, count as 1 char each — use them generously |

## Post Structure

Every post has two parts:

### Part 1: Hook (≤300 chars recommended)
- Shows in feed — this is the only thing most people see
- Must earn the "See more" tap on its own
- Short. Punchy. One idea. One line break at most.
- **Never start with "I"**
- **Never use**: "game-changing", "revolutionize", "democratize", "next-level", "unlock"

### Part 2: Body (reply or text attachment)
- Expands on the hook — the actual story
- Short paragraphs, one idea per block, lots of line breaks
- End with a **specific, high-friction question** — not "what do you think?"
  - Bad: "What do you think about AI trading?"
  - Good: "Would you let an AI reject a trade based on a news headline you haven't read yet?"

## Tone Guide

**Target voice**: Builder who made something that surprised even them. Dry, self-aware, proud but not
loud. The humor comes from specificity and absurdity — not jokes or puns.

**Think**: someone live-tweeting their own chaos. Not a product pitch. Not a LinkedIn post.

**DO:**
- Short sentences. Single thoughts per line.
- Let the details be funny (24.5 shares, four partial fills, three seconds)
- Name the weird thing you built without explaining why it's cool
- Use "the system", "the bot", "it" — not "my revolutionary AI"

**DON'T:**
- Hype language or buzzwords
- Emoji soup
- Wall-of-text paragraphs
- Explain the joke
- Sound like you're pitching to investors

## Output Format

Always return two clearly labeled sections:

```
**HOOK** (paste into Threads composer)
[hook text — ≤300 chars]

**BODY** (post as reply to your own post)
[body text — as long as needed, ends with a question]

**TAG:** [single tag phrase]
```

## Examples

### Example: Trade signal post

**Input context:**
- System went LONG TSLA at $406, 72% confidence
- AI ran bull vs bear debate reading live news headlines
- Every signal and chart gets posted to Discord automatically

**HOOK:**
> Built a Discord war room where two AIs argue about $TSLA before my bot decides to buy it.
>
> They just went long at $406. A chart auto-generated mid-debate. The bull agent won. 🤖

**BODY:**
> The system feeds live news into two LLMs — one perma-bull, one perma-bear.
>
> They argue. Out loud. In structured prose. With citations.
>
> Today the bull cited Tesla's China sales rebound and AI development tailwinds. The bear cited a 24% historical win rate and "general vibes of geopolitical uncertainty."
>
> The judge gave it 72% confidence and sent the order.
>
> $TSLA long at $406. 24.5 shares. Four partial fills in three seconds.
>
> Every signal gets piped into a private Discord channel in real time — bull case, bear case, ruling, rationale. A chart auto-generates and drops in the moment the order fires.
>
> It looks like a hedge fund ops center. It is one guy in a home office.
>
> Would you let an AI argue itself into a trade on your behalf, or does the 24% win rate footnote change your answer?

**TAG:** Algorithmic Trading

---

### Example: Build update post

**Input context:**
- Just shipped a feature: backend generates candlestick charts on the fly when a trade fires

**HOOK:**
> The bot now draws its own charts at the moment it trades.
>
> Did not expect "generate a PNG at 14:52:06" to feel this satisfying.

**BODY:**
> Every time a signal fires, the system renders a candlestick chart of exactly what it was looking at — VWAP, EMA crossover, RSI, the news headlines that tipped the decision.
>
> Then it drops the chart into Discord alongside the full AI debate log.
>
> The order fills. The chart appears. The bot explains itself.
>
> It's the closest thing to a trade confirmation that makes you feel like you understand what happened.
>
> Most people screenshot their broker app after the fact. This generates the evidence before the fill.
>
> At what point does a trading bot's audit trail become more trustworthy than a human's memory of why they clicked buy?

**TAG:** Algorithmic Trading

## Workflow When Used as a Skill

1. **Read the raw content** — log output, idea, build update, whatever the user provides
2. **Extract the interesting thing** — the surprising detail, the absurd specificity, the unexpected outcome
3. **Write the hook** — lead with the interesting thing, not the explanation
4. **Write the body** — tell the story in short bursts, end with a specific question
5. **Return both sections** clearly labeled with the tag

Do not ask for clarification unless the content is completely ambiguous. Default to the dry, builder-proud tone and let the user adjust.
