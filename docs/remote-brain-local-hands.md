# Remote Brain, Local Hands

The design `hermes-hands` is built around — one brain on a server, hands on the
machine you work on that carry no model and make no decisions.

---

One assistant should do three things at once, and the three fight each other.

**One:** it should *know your world* — your repositories, the decisions behind
them, the problems already solved — and keep knowing more the longer it is used.
**Two:** it should be reachable from anywhere — a terminal, a phone, a browser
tab. **Three:** it should actually *do* things where you work: run a `make`
target in a checked-out repo, edit a file in the tree you have open right now, not
in a sandbox three network hops away.

A single always-on service on a server nails one and two and completely fails
three: it has no hands in your working directory. A single agent on your laptop
nails three and fails one and two: it forgets everything when it closes, and
nothing else can reach it. Run both and you get two assistants with two separate
memories that slowly start disagreeing about what was decided.

So the design question is never *which agent*. It is: how do you split one
assistant across two machines without splitting its memory — or its judgement.

## The shape of it

One brain on a server. Hands on your machine that carry no model and make no
decisions. Exactly one memory — and every model call in the system goes through
one gateway, all of them from the brain. The hands make none.

```
you type ─▶ hermes-hands ─▶ Hermes   (plans, decides, remembers; one facts memory)
            (the hands)   ◀── {"calls":[{"tool":"local_shell",…}],"final":null} ── Hermes
            │
            └─ runs the call in your repo (approval for run/write), streams the
               raw {"results":[…]} back, loops, until Hermes returns "final".

   every model call is Hermes ─▶ one gateway ─▶ the model.   the hands run no model.
```

### the brain — Hermes

The brain is [NousResearch's Hermes Agent](https://github.com/NousResearch/hermes-agent),
running as a gateway process on a server. It holds the system prompt, the skills,
the model routing, and the part that actually matters: what it has learned — the
ongoing work, the decisions and their reasons, the shape of problems already
solved.

Everything you'd otherwise re-explain at the start of every session lives here.
Ask *"have we hit this failure before"* and the brain answers — because the brain
is the thing that was there the last time.

It can have doors that aren't a terminal — a chat bridge, a browser UI. Same
brain, same memory, whichever way you come in.

> A brain that forgets is just a chatbot with good manners.

### the memory

The memory is an external long-term store with vector search. The important thing
is that it is **not a transcript log**:

- Hermes distills what happened into clean, standalone facts — not *"then the user
  said…"* but *"the API moved to v2 in July; the v1 endpoints were retired."*
- The store embeds each fact and keeps the vector alongside it.
- On recall it runs a hybrid search, reranks the candidates and hands back the
  best few.

That rerank step is why the memory feels like memory. Ask *"what did we decide
about X"* and you get three correct facts, not two hundred lines of old chat to
skim. A strong model with mediocre recall over your history is worse than a weaker
model with none, because the mediocre one is confidently wrong about your own
project.

### the gateway

Every model call in the system — Hermes reasoning, the memory's embedding and
reranking — goes through one OpenAI-compatible gateway. The hands are the
exception that proves the rule: they call no model at all, so they hold no
provider key and no model address either.

One place for keys. One place for rate limits and spend. One place that knows
which model serves what. Move a model, and nothing downstream changes.

### the model

Tested with `qwen3.6-35b-a3b`; it currently runs best on `qwen3.8-flash-next`.

### the hands — hermes-hands

The hands are `hermes-hands`, this repo: a small program on the machine where you
actually work. One static binary, four tools — `local_read`, `local_write`,
`local_edit`, and `local_shell` for commands. What it is *not* is an agent: no
model, no plan mode, no task list, no loop of its own. That's the whole idea.

The loop lives entirely upstream. The brain replies with one instruction, as JSON:

```json
{"calls":[{"id":"c1","tool":"local_shell","args":{"cmd":"git status -s"}}],"final":null}
```

`hermes-hands` runs it and streams the raw result back, and the brain sends the
next instruction or a `final` answer. The shell is a single persistent
`bash --login` rooted at the working directory, so `cd` and environment persist
across calls; the file tools are plain, path-jailed reads and writes. The hands
never read the output they return. Nothing on that machine is deciding anything.

<!-- screenshot: the hermes-hands REPL mid-task — the animated "working · Ns"
     line, a tool call scrolling past, the brain's final answer. Add later. -->

The envelope shape isn't ad-hoc. It's deliberately the same one Hermes' own
unmerged "split runtime" proposal uses
([issue #18715](https://github.com/NousResearch/hermes-agent/issues/18715) /
PR #63966, still open). `hermes-hands` rides the Runs API that already ships; if
that proposal lands, the local half doesn't change — the transport gets tighter,
the dispatcher stays put. Until then, this bridge is the whole feature.

Because the hands act on a real machine, the boundary is where the care goes:
`local_shell`, `local_write` and `local_edit` ask before they run; there's a
denylist for the obvious footguns; the file tools are jailed to the working tree;
tool output is scrubbed for secrets before it crosses the wire; the connection to
the brain is HTTPS-only; and the one credential it holds sits in an
at-rest-encrypted store bound to the machine.

No opinions, no memory. Its only local state is its own session transcripts on
disk — enough to resume a task, not enough to form a worldview. The worldview
stays in the brain. When the brain trips on the same kind of step twice, it writes
the durable fix into the shared instructions and its own memory. The hands have
nothing to tune, because they were never the thing deciding.

## How you reach it

- **At the desk:** `hermes-hands` in a terminal — a small REPL — or an editor
  wired to its `--rpc` mode. Describe a change; the brain drives it in the tree
  you have open; the hands make the edit and report back. If the brain needs to
  look before it leaps, its first instruction is a look — an `ls`, a
  `local_read`.
- **Anywhere else:** a phone or a browser tab talks straight to the brain —
  *"what's the state of the project," "why did we hold that version back,"
  "draft the notes for the thing we discussed."* No hands needed for those.

Every entry point lands on the same brain and the same memory store. There is no
laptop assistant and phone assistant. There's one assistant with several kinds of
doors.

> The thing that answers on the phone is the thing that watched the bug get fixed
> last week.

## What this actually solves

- **Knowledge that compounds.** Months in, the brain doesn't need the project
  re-explained — it knows the layout, the incidents, the decisions and the reasons
  behind them. Every debugging session leaves it a little sharper.
- **One identity, everywhere.** Same memory, same context, same assistant,
  whichever door.
- **Hands where the work is.** Repo tasks run in the actual working tree with the
  actual toolchain, not in a sandbox that's almost-but-not-quite your environment.
- **No second memory to drift.** The hands keep no state and run no model, so
  nothing on the laptop can slowly start disagreeing with the brain — there's
  nothing down there with an opinion to disagree *with*.

Why the hands are *this* stripped down: wire two full agents together and you get
two reasoning loops and two memories that drift apart — the exact failure this
design is built against. So the hands aren't a smaller agent; they're not an
agent. Instructions go down, raw output comes back up, and there is no third
channel for a second opinion.

## The unglamorous part

None of what makes this feel like it *knows you* is clever. It's fact hygiene —
writing down standalone truths instead of hoping a transcript search turns them up
later. It's one gateway instead of five sets of credentials. It's one memory
instead of two. It's hands that carry no model and no memory, so there's nothing
down there to put in charge.

The interesting part was never the model. It was deciding what remembers, what
decides, and what just does as it's told — and then not letting those three roles
blur back together.
