# Simulation examples

Synthetic graphs with models and scenarios. They are the same cases the engine is tested
against (`mcp-server/internal/engines/sim/testdata/conformance`), so the numbers below are exact.

| Example | Graph | What it shows | Result |
|---|---|---|---|
| `sample01` | product, three clients, one executor | price and AI scenarios, a FIFO queue with a waiting deadline, money conserved | margin 80 / 120 / 75 / 165 for price 100 or 80, human or AI; with a random client B, C leaves in about half of the worlds |
| `warehouse` | supplier → warehouse → shop → customers | a graph without money: items conserved, loader hours added | slow deliveries lose sales; small batches double the loader's hours |
| `process_flow` | a small loan process (9 steps, loops, an exception path) | applications walking a process graph, manual steps waiting for officers | throughput and cycle time per staffing level; exception share over 40 random runs |

Run one offline:

    simulationRun(modelPath: "…/sample01/model.yaml", scenariosPath: "…/sample01/scenarios.yaml",
                  graphPath: "…/sample01/graph.yaml")

Paths are relative to the working directory: copy the example folder there first.
The models run on live layers too: pass `layerId` instead of `graphPath`.
