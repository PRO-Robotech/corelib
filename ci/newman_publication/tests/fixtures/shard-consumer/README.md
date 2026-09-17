# Independent shard verdict fixture provenance

The two Python files are byte-for-byte copies of the real Kacho producer and
direct aggregate reader at `b5fa093341f1fdbe96adfc6a9555482690968253`.
`provenance.json` binds their paths and SHA256 digests. Do not edit their logic
to make the publication holder pass. The other three actual Newman readers and
four captured Newman runs retain their existing `fixtures/provenance.json`.

The independent holder is `../../test_verdict_projection.py`, with fixture
construction in `../../shard_support.py`. It uses a real temporary Git repository,
tracked `deploy/e2e-shards.json` and tracked service collection paths. It invokes
the actual producer CLI and actual aggregate CLI, then records the reader's own
counts, category and exit. It does not implement a replacement decision table.

Routine binding v2 is recorded by the owner at
https://github.com/PRO-Robotech/kacho/issues/1810#issuecomment-5716515111,
subject SHA256 `19a9d04459011d47801d760db8d2d6fdecb84a01a5aca393973e3dc1fd3f0ad0`.
Its fixed catalogue selection, closed projected schema and full report census
extend no accepted scenario. Multiple selected shard collection sets must cover
all declared reports exactly once. `run.shard_index` is not a catalogue ordinal.
Successful publication has `missing=[]`; missing raw inputs are refusal cases.
`empty` preserves zero-assertion report indices and their real reader outcome.
The checker validates allowed precondition shape, not original raw-mark history.

The eight prerequisites cover four captured Newman scenes, both actual stand
precondition producers, a documented assertion-removal derivative, and two
distinct selected shards/reports. The last derivative is not claimed as a new
Newman execution. The Newman precondition fixture has suite exit 3 and actual
aggregate category `КРАСНЫЙ`; both observations remain visible.

Before the first nonempty verdict SUT assertion, every fixture runs the existing
no-verdict project/check and actual readers. The holder then checks real
nonempty project output separately from a lawful authored final ZIP. The latter
combines actual projected report bytes with the agreed verdict schema populated
from observed producer facts. Its actual aggregate observation must equal the
raw observation. It is a checker fixture, not proof that project produced it.

Raw, source and final mutants have recorded deltas. Final schema/counter mutants
rebind only auxiliary member digest/size so hashing cannot hide missing semantic
validation. No negative is counted as established until its lawful pair passes.
Missing capability on the lawful pairs is RED after valid prerequisites; broken
fixture/runtime is a prerequisite failure. The JSON summary separates these
from successful negative pairs and records every actual CLI result/capture.

The six accepted budgets are exercised without changing ceilings. Archive size,
expanded size, entry count and largest raw document use exact equality/one-below
pairs; decode depth uses 8/9 unwraps. Node accounting uses a bounded lawful list
and a list larger than the 100000-node ceiling, including opaque stripped input.
It does not claim that the latter is an exact one-node boundary across all
earlier report traversal. Earlier common scanner holders retain their separate
node-boundary obligations.

Run from the corelib root with the test's interpreter and no inherited runner
credentials, `GOWORK=off`, and an external temporary directory:

```sh
CI_NP_EVIDENCE_DIR=/external/private/evidence \
  python3 -B ci/newman_publication/tests/test_verdict_projection.py
```

This is test-only evidence for the local projection/check boundary. Consumer
workflows, pinned module delivery, SDK upload, historical retention, full runtime,
main ancestry and issue closure remain separate obligations.
