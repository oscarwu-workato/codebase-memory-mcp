"""GPU Louvain sidecar: reads a graph from stdin, runs cuGraph Louvain, writes partition to stdout.

STDIN protocol:
    <num_nodes> <num_edges>
    <node_id_1>
    ...
    <src_1> <dst_1>
    ...

STDOUT protocol:
    <node_id> <community>
    ...
"""

import sys
import time


def main() -> None:
    lines = sys.stdin.read().splitlines()
    idx = 0

    if not lines:
        print("empty stdin", file=sys.stderr)
        sys.exit(1)

    try:
        header = lines[idx].split()
        idx += 1
        num_nodes = int(header[0])
        num_edges = int(header[1])
    except (IndexError, ValueError) as exc:
        print(f"parse error in header: {exc}", file=sys.stderr)
        sys.exit(1)

    if num_nodes < 3 or num_edges < 3:
        print("GPU_LOUVAIN_TOO_SMALL", file=sys.stderr)
        sys.exit(2)

    node_ids: list[int] = []
    try:
        for _ in range(num_nodes):
            node_ids.append(int(lines[idx].strip()))
            idx += 1
    except (IndexError, ValueError) as exc:
        print(f"parse error reading node IDs: {exc}", file=sys.stderr)
        sys.exit(1)

    src_ids: list[int] = []
    dst_ids: list[int] = []
    try:
        for _ in range(num_edges):
            parts = lines[idx].split()
            idx += 1
            src_ids.append(int(parts[0]))
            dst_ids.append(int(parts[1]))
    except (IndexError, ValueError) as exc:
        print(f"parse error reading edges: {exc}", file=sys.stderr)
        sys.exit(1)

    try:
        import cudf  # type: ignore[import-untyped]
        import cugraph  # type: ignore[import-untyped]
    except ImportError as exc:
        print(f"GPU_UNAVAILABLE: {exc}", file=sys.stderr)
        sys.exit(1)

    t_start = time.monotonic()

    df = cudf.DataFrame({"src": src_ids, "dst": dst_ids})
    g = cugraph.Graph()
    g.from_cudf_edgelist(df, source="src", destination="dst")
    parts, _ = cugraph.louvain(g, max_level=15, threshold=0.0001)

    result = parts.to_pandas()
    communities: set[int] = set()
    for _, row in result.iterrows():
        community = int(row["partition"])
        communities.add(community)
        print(int(row["vertex"]), community)

    elapsed_ms = (time.monotonic() - t_start) * 1000
    print(
        f"GPU_LOUVAIN nodes={num_nodes} edges={num_edges}"
        f" communities={len(communities)} time_ms={elapsed_ms:.1f}",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
