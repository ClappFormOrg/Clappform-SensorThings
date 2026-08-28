# Ready-to-post discussion drafts: Geonovum testbed-sensordata-2026

Working file. Nothing here has been posted. Each block below is a complete post body; copy from
under the `PASTE FROM HERE` line. Target repo:
<https://github.com/Geonovum/testbed-sensordata-2026/discussions>

---

## Gap analysis: what is already on the board

Checked all 19 discussions and 1 issue on 2026-07-31.

**Already reported, no new post needed**

| Our finding | Where | Status |
|---|---|---|
| `Sensor.metadata` must be a string (our 4.7) | #10, our own comment of 22 Jul | Posted but **unanswered**, and our own conclusion was hedged and partly wrong -> post **1** is a follow-up |
| WBD server slow / timing out (our 4.5, network side) | #18 by `mathisvfr` | Posted; `hylkevds` could not reproduce |
| Dual-target datastream id mapping (our step 7) | #11 by `mathisvfr` | Posted and answered: resolve by query, do not hard-code ids, which is what we do |
| Collaborall `observationType` ValueCode strictness | #10 by `justinschembri` | Reported by others |
| Collaborall `unitOfMeasurement.symbol` required | #10 by `justinschembri` | Reported by others, marked closed |
| Collaborall `$expand`, `$select=properties`, `substringof`, `POST /Datastreams(n)/Observations` | #10 | Reported by others, several already fixed by `Kattouw` |
| WBD custom `Projects` model and anonymous read | #6, #8 | Documented by `hylkevds` |
| External identifiers / UUIDs as first-class fields | #12 by `justinschembri` | Adjacent to our 6.2 but a different ask: theirs is about external references, ours is about uniqueness constraints |
| `phenomenonTime` for high-rate sampling | #15 by `justinschembri` | Adjacent to our polymorphism note, different problem |
| Extensions becoming official | #20 by `justinschembri` | Adjacent to post **5** below, which complements it |
| Connector / data provenance | #16 by `Kattouw` | Adjacent to our `resultQuality` usage, different scope |

**Not reported anywhere, posts 1 to 6 below**

1. Follow-up in #10: the `metadata` question is answerable from the spec (and we had it wrong)
2. No uniqueness constraint makes name-keyed upsert silently racy
3. No observation-level idempotency, and MQTT publish has no ack
4. Semantic interoperability: `definition` and `unitOfMeasurement` need binding
5. A machine-readable capabilities document
6. Synthesis: telling implementation bugs, deployment quirks and spec gaps apart

Suggested posting order: **1** first (it closes an open thread of ours), then **4** (highest value,
and it is our lead recommendation), then **2** and **3**, then **5** and **6**.

---

## Post 1: reply inside discussion #10

**Where:** comment in the existing thread
[#10 Bug Report & Resolving collaborall STA Server](https://github.com/Geonovum/testbed-sensordata-2026/discussions/10),
as a reply to our own 22 July comment.

PASTE FROM HERE

Following up on my own report from 22 July about `POST /Sensors` being rejected with
`STA-422, "The metadata field must be a string."`

I hedged in that comment, guessing the server might be right to insist on a string. I went and read
the normative text, and I no longer think that is the case. Two corrections to my original post.

**First, the specification permits what we sent.** OGC 18-088 Table 13 types `Sensor.metadata` as:

> **Any** (depending on the value of the encodingType), One (mandatory)

Table 15 lists three encodingTypes (`application/pdf`, SensorML, `text/html`), and the prose that
follows is explicit that the list is not closed:

> Other encodingTypes are permitted (e.g., `text/plain`). Note that the metadata property may
> contain either a URL to metadata content ... **or the metadata content itself** (in the case of
> `text/plain` or other encodingTypes that can be represented as valid JSON).

An inline JSON object under `encodingType: application/json` is metadata content that can be
represented as valid JSON. So nothing in the standard requires `metadata` to be a string, and a hard
rejection is enforcing a constraint the spec does not state.

**Second, I called it "the Collaborall FROST server" and that was wrong.** As #20 notes, it is an
independent implementation, not a FROST build. That actually makes this more interesting rather than
less: the disagreement between the two servers is a disagreement between two independent readings of
one specification, which is exactly the kind of thing this testbed is for.

For what it is worth, `"The metadata field must be a string."` is the verbatim message of Laravel's
built-in `string` validation rule, so I would guess this is a single line in a request-validation
schema rather than a deliberate interpretation, and an easy call to make, given all three enumerated
encodingTypes do carry string-shaped content.

**Where I think the ambiguity genuinely is:** the same passage ends with "It is up to clients to
perform *string parsing* necessary to properly handle metadata content", which does read as though
metadata is always a string. Declared type `Any`, prose that assumes a string. That is worth a
clarification request to the SWG regardless of what any server does, and I have written it up separately.

@Kattouw, would you be able to relax the `metadata` validation to accept an object as well as a
string? On our side we are making the serialisation per-target so we can keep sending objects to
servers that accept them, rather than degrading globally to strings.

---

## Post 2: new discussion

**Category:** General
**Title:** No uniqueness constraint on entity names makes name-keyed upsert silently racy

PASTE FROM HERE

A finding from our translation layer that I think applies to anyone writing to STA concurrently, and
that produced wrong data with no error of any kind.

**The pattern.** Server-side ids are server-generated, so a client that has to be restart-safe
cannot store them as its key. The advice in #11 applies: resolve entities by querying for them.
That means every writer implements the same upsert: `GET ?$filter=name eq '<x>'`, then `POST` if
absent.

**The problem.** That is check-then-act, and STA does not require entity names to be unique. So when
two requests race, both find the entity absent and both create it, and the server is right not to
complain. We resolve datastreams in parallel, and entities that are *shared* between datastreams
(one `Sensor` per sensor model, one `ObservedProperty` per phenomenon) are exactly the ones that
race. We ended up with five copies each of one Sensor and one ObservedProperty.

**Why this is worth raising rather than just fixing.** No 409, no error, no signal. We only noticed
because a later name lookup returned five hits where it expected one, and we happened to have
written that lookup to log multiplicity. Implemented the obvious way, taking the first result, this
stays invisible forever. Data is silently duplicated and every downstream consumer sees a plausible
answer.

We fixed it on our side with a per-key single-flight around the create, so concurrent first-sight
resolutions of one name collapse into one lookup and one POST. That works, but it is a client-side
fix for something the server could settle definitively.

**What would remove the class of bug,** roughly in order of preference:

1. A standard **idempotent create**: a client-supplied natural key, or an `If-None-Match`-style
   conditional POST, returning the existing entity instead of a duplicate.
2. A way for a server to **declare a uniqueness constraint** (e.g. `Sensor.name` is unique) and
   return 409 on violation. Even just this turns silent corruption into a loud error a client can
   converge on.
3. Failing both, a **documented upsert-by-name pattern** in the spec, so implementers at least hit
   the race knowingly.

**Related, and a reason not to solve it with names alone:** on a shared server, name-keyed upsert
also means our lookup can bind to *another participant's* entity that happens to share a name,
inheriting their definition and units silently. We prefix every entity name we create to avoid it.
That works, but it guarantees fragmentation: if two parties genuinely should converge on one
`Fill level` concept, prefixing ensures they never do. Namespacing on shared servers might be worth
a convention at programme level.

Has anyone else writing concurrently hit this? @hylkevds, is client-defined id / uniqueness
something v2 addresses?

---

## Post 3: new discussion

**Category:** General
**Title:** Observation idempotency is entirely the client's problem, and it costs a round-trip per write

PASTE FROM HERE

Companion to the entity-uniqueness thread. Same root cause, different altitude, and this one has a
throughput consequence we ran into during back-fill.

**The situation.** `(Datastream, phenomenonTime)` is a natural key in every deployment I have seen,
but nothing enforces it. Re-polling a vendor API, back-filling a gap, or retrying after a timeout
can all duplicate observations. So every producer that wants to be safe builds the same two things:
a persistent write-log, and a check before each write.

We have both: a Postgres write-log keyed on `(datastream_id, phenomenon_time)`, and a
`GET .../Observations?$filter=phenomenonTime eq ...` probe before each write. It works: a multi-day
back-fill after a restart produced a contiguous series with no duplicates.

**The cost, which surprised us.** We support two transports: HTTP POST and MQTT publish. On the MQTT
path the write is asynchronous and fast, but the *safety probe* in front of it is a synchronous HTTP
round-trip. So the fast transport is gated by the slow protocol. During back-fill over a
higher-latency path, the probe, not the publish, was the bottleneck, repeatedly hitting our client
timeout while publishes kept pace easily. Adding MQTT capacity does nothing, because the queue forms
in front of the probe. It also reads like a transport failure and is not one.

**A second, smaller issue in the same area:** MQTT publish is fire-and-forget. There is no ack and no
server response, so a producer cannot tell whether a publish persisted, and the only way to verify is
a REST read-back. That is defensible protocol design, but it means the MQTT write path cannot report
its own success and monitoring has to be built separately.

**What would help:**

- An optional but **discoverable dedupe-on-natural-key** for Observations, where the server ignores
  or updates a duplicate `(Datastream, phenomenonTime)` rather than creating a second row. That
  removes the probe entirely.
- Some form of **acknowledgement on the MQTT path**, so a publisher can distinguish delivered from
  dropped without a REST round-trip.

To be clear this is not a criticism of either server we used, since both behave correctly. The
specification is not ambiguous here, it is silent, and the cost of that silence is a round-trip per
observation paid independently by every producer.

Curious whether others have solved this differently, or whether anyone is relying on the Data Array
extension for bulk insert and getting different performance characteristics.

---

## Post 4: new discussion

**Category:** Ideas
**Title:** Syntactic interoperability is solved; semantic interoperability is not. A case for binding definitions and units.

PASTE FROM HERE

This is the finding I would most like to see the programme act on, and it came from replicating
another participant's server rather than from writing to one.

We pointed a generic reader at a testbed STA server we knew nothing about: no documentation, no
schema, no conversation with its operators, and we successfully replicated its entity graph in a couple
of days. That is a genuine success for the standard and I do not want to undersell it: 46
ObservedProperties, 460 Datastreams, in a domain (pump telemetry, seismics, air quality) with no
overlap with our own waste containers.

**Then we tried to make the data mean something, and stopped.** In that one server there are several
distinct `co2_levels` ObservedProperties with different `@iot.id`s and different `definition` URIs,
drawn variously from QUDT, Wikipedia, DBpedia and CF conventions. Several distinct
`gauge_pressure`, `battery_level` and `temperature_indoor` entries likewise. Some
`unitOfMeasurement` values are placeholders, literally `"..."`, or null.

None of this violates STA. The server is well-formed and its data is valid. But a consumer cannot
answer *"give me all CO₂ readings in this area"*, not across servers, and not even within that one
server, without a human sitting down and curating a mapping by hand. That is the thing the standard
is supposed to make unnecessary.

**Why this is a profile problem rather than a spec bug.** `ObservedProperty.definition` is a free URI
and `unitOfMeasurement` is effectively free text, and that openness is deliberate and defensible,
the standard cannot enumerate every phenomenon in every domain. But it means the base standard
delivers syntactic interoperability and stops there. The semantic layer is exactly what a *national
profile* is well placed to specify, and doing so requires no change to OGC 18-088 at all.

**Concretely, what we would propose for a Dutch STA profile:**

- **A mandated controlled vocabulary per domain** for `ObservedProperty.definition`, with the
  requirement that the URI resolves to a term in that vocabulary rather than to any page that
  describes the concept.
- **UCUM required** for `unitOfMeasurement.symbol`, and null only in the case the spec explicitly
  allows (no unit of measurement, e.g. `OM_TruthObservation`), not as a placeholder.
- **A conformance checker** for the profile, so "profile-compliant" is a testable claim and not an
  intention.

Of everything in our testbed report this is the item with the best ratio of value to effort, because
it is the layer the base standard deliberately leaves open and the layer where a national ecosystem
gains most from simply agreeing.

Would others find a profile like that usable, and are there existing Dutch or European vocabularies
we should be starting from rather than inventing?

---

## Post 5: new discussion

**Category:** Ideas
**Title:** Could servers expose a machine-readable capabilities document?

PASTE FROM HERE

A small ask with, I think, a disproportionate payoff. Partly prompted by #20 on extensions, and by
#6 documenting the Brabantse Delta server's data model in prose.

Across two servers in this testbed, here is what we had to establish by probing, one request at a
time:

- which extensions are present, and which entity sets actually exist (#10 notes the landing page and
  the real entity sets do not always agree)
- whether MQTT is available, and at which URL
- which auth scheme applies, and whether reads are authenticated as well as writes
- whether duplicate entity names are rejected
- the page size, and whether `$expand` / `$select` / `substringof` are implemented
- whether the deployment is a stock build or carries custom entities and an authorization model

Every one of those was a manual investigation, and getting one wrong has real cost: another
participant in #6 registered a batch of Things before discovering the server's Project model, then
had to patch every one of them.

OGC API - Features solved this with a required `/conformance` endpoint. STA has no equivalent: the
service root lists collections and little else, so a generic client cannot adapt and a human has to
read a discussion thread instead.

**The ask:** a required, machine-readable capabilities document declaring supported conformance
classes and extensions, available transports, auth scheme(s), limits, and any non-core entity sets.

It is probably the cheapest item on our whole improvement list to specify, and it is the one that
most directly reduces the human effort of onboarding onto an unfamiliar server. It would also give
#20's question a concrete answer: extensions can stay optional as long as they are *discoverable*,
because then binding to one is an informed choice rather than an accident.

@hylkevds, is anything along these lines under discussion for v2?

---

## Post 6: new discussion

**Category:** General
**Title:** Telling the three apart: implementation bug, deployment quirk, or gap in the standard?

PASTE FROM HERE

Reading back through #10, I noticed that several of us have reported "server X accepts this and
server Y rejects it" findings, and that we have been reaching for different explanations without
making the distinction explicit. Having now been on both sides of it, I think the distinction matters
a lot, because the three diagnoses have completely different remedies on completely different
timescales.

There are three possible verdicts, not two:

| Verdict | Test | Remedy | Timescale |
|---|---|---|---|
| **A. The lax server is wrong** | Spec mandates a constraint; server accepts violations | Report to that server; fix your own payload, which was silently invalid | Weeks, and your existing data may need correcting |
| **B. The strict server is wrong** | Spec permits the payload; server rejects it | Report to that server; work around per-target meanwhile | Weeks |
| **C. The spec is ambiguous** | The text can be read both ways in good faith | Clarify the spec; both servers stay defensible until then | Years |

The test is always the same, and it is not "which server is more widely used" or "which one did I
develop against": **read the normative text and establish whether it mandates, permits, or is
silent.**

Three cases from this testbed land in three different rows:

- **`observationType: "instant"`** (#10, @justinschembri): the spec types `observationType` as a
  mandatory ValueCode from an enumerated list, so `"instant"` was never valid. FROST accepted it,
  Collaborall rejected it. **Verdict A: the strict server was right,** and a client that developed
  against the lax one had been building on laxity without knowing.
- **`Sensor.metadata` as a JSON object** (our report in #10): the spec types it `Any` and explicitly
  contemplates inline JSON content. **Verdict B: the strict server is wrong here**, the mirror image
  of the previous case, on the same server.
- **`unitOfMeasurement` with a null `symbol`** (#10, @justinschembri): the spec says a Datastream
  with no unit of measurement SHALL have null unitOfMeasurement properties, so null is permitted and
  rejecting it is over-strict. But the spec genuinely does not say what a *partially* null unit means,
  which was the actual case. **Part B, part C.**

Two things follow.

**One: "just make the server happy" is the expensive option.** It is what everyone does under
deadline, us included, and it is how an unstated constraint becomes the de facto standard. If every
client degrades to the strictest server it encounters, the strict behaviour propagates, the
loose-but-legal form dies out, and the spec's actual permission becomes untestable because nothing
exercises it any more. Where we have had to work around something, we have made the workaround
per-target rather than global, specifically to avoid this.

**Two: a published interoperability test suite would settle all three rows at once.** Every case
above would have been caught by a suite asserting what a server must accept *and* what it must
reject, including the ambiguous cases as explicit known-ambiguous entries. Of everything discussed
in this testbed, that feels like the highest-leverage deliverable, because it converts prose into
something a server either passes or fails, and it is the only remedy that scales past the people
currently in this thread.

Would there be appetite for assembling one from the cases we have collectively found here? Between
#10 and the various server-specific threads we have a decent starting corpus already.

---

## Post 7: reply to the uniqueness and idempotency answer

**Where:** as a threaded reply under whichever of posts 2 or 3 drew the response. It covers both,
so post it once, in the thread that received the answer, and cross-link the other.

PASTE FROM HERE

This is the most useful reply we could have had, and it corrects us on one point, so let me take
that first.

**We were wrong that a datastream and a timestamp form a natural key.** Our write-up says exactly
that, "in every real deployment", and your lab-analysis example refutes it. Repeated analyses of one
sample at the same phenomenonTime with different parameters is a legitimate shape, and it is a shape
in the water domain, which is the domain of the very server we write to. So a mandatory
dedupe-on-natural-key would not be a safety improvement, it would break a valid use case. We are
correcting that claim rather than softening it.

What survives is narrower and, we think, still worth something. Not "the standard should enforce
uniqueness", which your three counter-examples rule out, but "a client should be able to find out
what this server enforces, and should have a portable way to create without first reading". Those two
are separable from uniqueness, and the rest of this reply is about them.

**On client-specified ids in v2, that is the answer to our entity-level problem.** If we can supply
the id, our upsert stops being check-then-act at all. We derive an id deterministically from the
vendor key, write at that id, and the primary key does the deduplication that no amount of
client-side locking can guarantee. That removes a round-trip per entity as well as the race. Three
questions about it:

1. Is it discoverable? "A service may allow users to specify a value" is exactly the kind of
   optional behaviour a portable client currently has to establish by trial. Is there a conformance
   class for it in v2, or something in the landing document that says yes or no?
2. Is there create-or-replace at a known id, so a client can be idempotent in one request rather
   than handling the "already exists" error as a normal path?
3. How does this sit against the `definition` field you mentioned in #12 for external references? If
   we already hold a stable vendor key, is the recommendation to put it in the id, or to keep
   server-generated ids and carry the vendor key in `definition`?

**On server-side constraints, that relocates our recommendation rather than removing it, and we
think in a useful direction.** You are right that an admin can add constraints after talking to
domain experts, including on `properties`, and that this project could have done so. For the shared
testbed instance, a unique constraint on `Sensor.name` and `ObservedProperty.name` would have turned
our silent duplication into a 409 on the first run, and we would support adding one if others agree.

The wider point we take from that: on a server several parties write into, which constraints exist is
a deployment decision that every writer depends on and none of them can see. That is not a gap in
the specification. It is a gap in what a national profile should pin down, and we are moving it there
in our report. It is also a second argument for a machine-readable capabilities document, since
admin-added constraints are precisely the kind of local truth a client cannot guess.

**On your import architecture, that is a better answer than the fix we had planned.** We probe once
per observation. You probe once per datastream, then rely on the ordering. We had already noted that
our stored cursor makes the probe redundant, but we had not made the connection to threading: if
inserting an Observation takes a lock on the Datastream, then per-datastream parallelism buys nothing
and the single-threaded design costs nothing. That also explains something we recorded as a puzzle,
namely that adding concurrency did not move our throughput. We are adopting the pattern: one thread
per datastream, one latest-observation query per run, then batch upload.

One case where we think we still need the write-log alongside it. Your invariant is that anything
later than the newest stored observation cannot exist yet, which holds when a source only ever moves
forward. Ours does not always. The vendor API mixes settled estimates with forecasts, and it can
re-issue a corrected value for a timestamp we have already passed. For those, "later than the
maximum" does not help and we still need per-key bookkeeping. Do you hit back-dated corrections in
your imports, and if so, do you handle them as a separate reconciliation pass rather than inside the
forward-only path?

**On batch upload:** good to know data arrays are what you use in practice. We listed bulk insert as
a minor gap because it is an extension rather than core, so a portable client cannot assume it. Your
answer suggests the honest framing is different: it is available where it matters and the real problem
is again that its presence is not discoverable.

Thanks for the detail. The v2 id change and the deployment-constraint route between them cover most
of what we were asking for, and both are more actionable than the specification change we proposed.
