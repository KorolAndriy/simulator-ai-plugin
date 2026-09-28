# Conformance cases

Each directory holds `graph.yaml`, `model.yaml`, `scenarios.yaml` and `expected.json`.
`TestConformance` runs every scenario and compares status, steps, pending events, metrics
and the processed events (`[time, priority, kind, target]`) with `expected.json`; a
scenario with `runs: N` is also compared as a many-run summary. The rules are in
`plugins/simulator/docs/simulation/model-format.md`, "Conformance".

`expected.json` comes from sim-morrow, the Python reference engine (Corezoid GitLab,
`simulator/sim-morrow`), where the same cases live in `conformance/`:

    uv run python scripts/export_conformance.py   # rewrites conformance/*/expected.json
    cp -r conformance/* <this directory>/

Without access to sim-morrow the cases still work as a regression suite: a change that
alters any expected event is a change of semantics and needs the spec, both engines and
the cases updated together.
