# Remote Brain, Local Hands

Field notes on the homelab setup `hermes-hands` was built for — one brain on the
server, hands on the laptop that carry no model and make no decisions.

---

I wanted one assistant that could do three things at once, and the three things
fight each other.

**One:** it should *know my world* — my repositories, the layout of my homelab,
the database incident that ate two hours, which model runs on which GPU and why —
and keep knowing more the longer I use it. **Two:** I should reach it from
anywhere — the terminal, the phone on the couch, a browser tab at work.
**Three:** it should actually *do* things where I work: run a `make` target in a
checked-out repo, edit a file in the tree I have open right now, not in a sandbox
three network hops away.

A single always-on service on a server nails one and two and completely fails
three: it has no hands in my working directory. A single agent on my laptop nails
three and fails one and two: it forgets everything when I close it, and the couch
can't reach it. Run both and you get two assistants with two separate memories
that slowly start disagreeing about what you decided.

So the design question was never *which agent*. It was: how do you split one
assistant across two machines without splitting its memory — or its judgement.

## The shape of it

One brain in the homelab. Hands on the laptop that carry no model and make no
decisions. Exactly one memory — and every model call in the system goes through
one gateway, all of them from the brain. The hands make none.

```
you type ─▶ hermes-hands ─▶ Hermes   (plans, decides, remembers; one facts memory)
            (the hands)   ◀── {"calls":[{"tool":"shell",…}],"final":null} ── Hermes
            │
            └─ runs the call in your repo (approval for run/write), streams the
               raw {"results":[…]} back, loops, until Hermes returns "final".

   every model call is Hermes ─▶ one gateway ─▶ a GPU box.   the hands run no model.
```

### the brain — Hermes

The brain is [NousResearch's Hermes Agent](https://github.com/NousResearch/hermes-agent),
running as a gateway process in a hardened container on a homelab VM — pinned by
digest, no host Docker socket, its own tool execution boxed into rootless Podman.
It holds the system prompt, the skills, the model routing, and the part that
actually matters: the accumulated picture of me. Interests, ongoing projects, the
topology of the homelab, the shape of problems I've already solved.

Everything I'd otherwise re-explain at the start of every session lives here. When
I ask *"have we hit this failure before,"* the brain answers — because the brain
is the thing that was there the last time.

It has two doors that aren't a terminal: a chat bridge to my phone, and a browser
UI on the LAN. Same brain, same memory, whichever way I come in.

> A brain that forgets is just a chatbot with good manners.

### the memory — Hindsight

The memory is [Hindsight](https://github.com/vectorize-io/hindsight), an external
long-term store with its own Postgres and pgvector, running the slim build — it
deliberately doesn't carry its own embedding model, it borrows the homelab's.

The important thing is that it is **not a transcript log**:

- Hermes distills what happened into clean, standalone facts — not *"then the user
  said…"* but *"the ai-rtx box has run qwen3.6-35b-a3b since late July; the earlier
  Gemma model was decommissioned."*
- Hindsight embeds each fact with a Qwen3 embedding model and stores the vector
  alongside it.
- On recall it runs a hybrid search, then reranks the top ~150 candidates with a
  Qwen3 reranker and hands back the best few.

That rerank step is why the memory feels like memory. Ask *"what did we decide
about X"* and you get three correct facts, not two hundred lines of old chat to
skim. A strong model with mediocre recall over your history is worse than a weaker
model with none, because the mediocre one is confidently wrong about your own
project.

### the gateway — Bifrost

Every model call in the system — Hermes reasoning, Hindsight's embedding and
reranking — goes through one OpenAI-compatible gateway,
[Bifrost](https://github.com/maximhq/bifrost). The hands are the exception that
proves the rule: they call no model at all, so they hold no provider key and no
GPU address either.

One place for keys. One place for rate limits and spend. One place that knows
which GPU serves which model. When I moved a model between boxes, nothing
downstream changed.

### the hardware, concretely

Nothing here is a rented API. Two GPU boxes in a rack, abstracted so completely by
the gateway that the rest of the system can't tell:

- **ai-rtx** runs the brain's chat model, `qwen3.6-35b-a3b` — a 35B
  mixture-of-experts, ~3B active per token, 131K context, on one 16&nbsp;GB
  RTX&nbsp;5060&nbsp;Ti. A MoE this size offloads cleanly, so 16&nbsp;GB of VRAM
  buys a distinctly smarter model than any dense one that fits without offload.
  The cost is prefill latency on long prompts; generation runs at conversational
  speed.
- **ai-quadro** runs the supporting cast: the embedding and reranker models
  Hindsight leans on, plus a small utility model. The hands used to borrow a 4B
  here — they don't run one any more.

### the hands — hermes-hands

The hands are `hermes-hands`, this repo: a small program on the machine where I
actually work. One static binary, four tools — read a file, write a file, edit a
file, run a shell command. What it is *not* is an agent: no model, no plan mode,
no task list, no loop of its own. That's the whole idea.

The loop lives entirely upstream. The brain replies with one instruction, as JSON:

```json
{"calls":[{"id":"c1","tool":"shell","args":{"cmd":"git status -s"}}],"final":null}
```

`hermes-hands` runs it and streams the raw result back, and the brain sends the
next instruction or a `final` answer. The shell is a single persistent
`bash --login` rooted at the working directory, so `cd` and environment persist
across calls; the file tools are plain, path-jailed reads and writes. The hands
never read the output they return. Nothing on the laptop is deciding anything.

<!-- screenshot: the hermes-hands REPL mid-task — the animated "working · Ns"
     line, a tool call scrolling past, the brain's final answer. Add later. -->

The envelope shape isn't ad-hoc. It's deliberately the same one Hermes' own
unmerged "split runtime" proposal uses
([issue #18715](https://github.com/NousResearch/hermes-agent/issues/18715) /
PR #63966, still open). `hermes-hands` rides the Runs API that already ships; if
that proposal lands, the local half doesn't change — the transport gets tighter,
the dispatcher stays put. Until then, this bridge is the whole feature.

Because the hands act on a real machine, the boundary is where the care goes:
`shell`, `write_file` and `edit_file` ask before they run; there's a denylist for
the obvious footguns; the file tools are jailed to the working tree; tool output
is scrubbed for secrets before it crosses the wire; the connection to the brain is
HTTPS-only; and the one credential it holds sits in an at-rest-encrypted store
bound to the machine.

No opinions, no memory. Its only local state is its own session transcripts on
disk — enough to resume a task, not enough to form a worldview. The worldview
stays in the brain. When the brain trips on the same kind of step twice, it writes
the durable fix into the shared instructions and its own memory. The hands have
nothing to tune, because they were never the thing deciding.

## How I reach it

- **At the desk:** `hermes-hands` in a terminal — a small REPL — or an editor
  wired to its `--rpc` mode. I describe a change; the brain drives it in the tree
  I have open; the hands make the edit and report back. If the brain needs to look
  before it leaps, its first instruction is a look — an `ls`, a `read_file`.
- **From the couch or the office:** the phone and a browser tab talk straight to
  the brain — *"what's the state of the stack," "why did we hold that version
  back," "draft the notes for the thing we discussed."* No hands needed for those.

Every entry point lands on the same brain and the same memory store. There is no
laptop assistant and phone assistant. There's one assistant with several kinds of
doors.

> The thing that answers on my phone is the thing that watched me fix the bug last
> week.

## What this actually solves

- **Knowledge that compounds.** Months in, the brain doesn't need the homelab
  re-explained — it knows the topology, the model layout, the incidents, the
  decisions and the reasons behind them. Every debugging session leaves it a
  little sharper.
- **One identity, everywhere.** Desk, couch, office — same memory, same context,
  same assistant.
- **Hands where the work is.** Repo tasks run in the actual working tree with the
  actual toolchain, not in a sandbox that's almost-but-not-quite my environment.
- **No second memory to drift.** The hands keep no state and run no model, so
  nothing on the laptop can slowly start disagreeing with the brain — there's
  nothing down there with an opinion to disagree *with*.

Why the hands are *this* stripped down: wire two full agents together and you get
two reasoning loops and two memories that drift apart — the exact failure I was
designing against. So the hands aren't a smaller agent; they're not an agent.
Instructions go down, raw output comes back up, and there is no third channel for
a second opinion.

## The unglamorous part

None of what makes this feel like it *knows me* is clever. It's fact hygiene —
writing down standalone truths instead of hoping a transcript search turns them up
later. It's one gateway instead of five sets of credentials. It's one memory
instead of two. It's hands that carry no model and no memory, so there's nothing
down there to put in charge.

The interesting part was never the model. It was deciding what remembers, what
decides, and what just does as it's told — and then not letting those three roles
blur back together.
