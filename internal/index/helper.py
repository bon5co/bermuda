"""Chroma, driven by bermuda.

Bermuda is a Go binary and Chroma is a Python library, so this script is the
whole of the seam between them: one request as JSON on stdin, one response
written to the file the request names, one process per call. There is no
server. Chroma runs embedded against a directory, which is what makes "no
daemon to install" still true of bermuda after this feature exists.

The response goes to a file rather than to stdout because stdout here belongs
to whatever Chroma, onnxruntime and the model downloader feel like printing,
and a progress bar in the middle of a JSON document is not recoverable.
"""

import json
import os
import sys


def respond(out, payload):
    with open(out, "w", encoding="utf-8") as fh:
        json.dump(payload, fh)


def collection(client, name):
    """Open the collection, cosine-spaced.

    MiniLM vectors are normalised, so cosine is the distance that matches how
    the model was trained; Chroma's default is L2. The two ranked results
    differently enough on the same query that it was worth pinning, and the
    configuration key for it moved between Chroma versions — hence the
    fallback rather than one call.
    """
    try:
        return client.get_or_create_collection(
            name, configuration={"hnsw": {"space": "cosine"}}
        )
    except TypeError:
        return client.get_or_create_collection(
            name, metadata={"hnsw:space": "cosine"}
        )


def main():
    req = json.load(sys.stdin)
    out = req["out"]
    try:
        import chromadb
        from chromadb.config import Settings

        client = chromadb.PersistentClient(
            path=req["dir"],
            # A memory index must not phone home about the memory it indexes.
            settings=Settings(anonymized_telemetry=False),
        )
        op = req["op"]

        if op == "drop":
            try:
                client.delete_collection(req["collection"])
            except Exception:
                pass  # dropping what was never created is the same outcome
            respond(out, {"ok": True})
            return

        col = collection(client, req["collection"])

        if op == "stats":
            respond(out, {"ok": True, "count": col.count(),
                          "chromadb": chromadb.__version__})
            return

        if op == "upsert":
            # Deletes first, and by path: a note that lost three paragraphs
            # must not leave those paragraphs searchable, and deleting by the
            # ids we are about to write would miss exactly them.
            paths = req.get("delete_paths") or []
            for i in range(0, len(paths), 200):
                col.delete(where={"path": {"$in": paths[i:i + 200]}})

            docs = req.get("docs") or []
            # Chroma caps a single add; 1000 is comfortably under every
            # version's limit and keeps peak memory flat on a small VM.
            for i in range(0, len(docs), 1000):
                batch = docs[i:i + 1000]
                col.upsert(
                    ids=[d["id"] for d in batch],
                    documents=[d["text"] for d in batch],
                    metadatas=[d["metadata"] for d in batch],
                )
            respond(out, {"ok": True, "deleted_paths": len(paths),
                          "upserted": len(docs), "count": col.count()})
            return

        if op == "query":
            where = req.get("where") or None
            res = col.query(
                query_texts=[req["text"]],
                n_results=int(req.get("n") or 8),
                where=where,
                include=["documents", "metadatas", "distances"],
            )
            hits = []
            ids = (res.get("ids") or [[]])[0]
            docs = (res.get("documents") or [[]])[0]
            metas = (res.get("metadatas") or [[]])[0]
            dists = (res.get("distances") or [[]])[0]
            for i in range(len(ids)):
                hits.append({
                    "id": ids[i],
                    "text": docs[i] if i < len(docs) else "",
                    "metadata": metas[i] if i < len(metas) else {},
                    "distance": dists[i] if i < len(dists) else None,
                })
            respond(out, {"ok": True, "hits": hits})
            return

        respond(out, {"ok": False, "error": "unknown op %r" % op})
    except Exception as exc:  # reported, not raised: the caller reads the file
        respond(out, {"ok": False, "error": "%s: %s" % (type(exc).__name__, exc)})
        sys.exit(1)


if __name__ == "__main__":
    if os.environ.get("BERMUDA_INDEX_HELPER_SELFTEST"):
        # Lets the Go tests prove the wiring — argv, stdin, the response file —
        # on a machine with no chromadb installed at all.
        req = json.load(sys.stdin)
        respond(req["out"], {"ok": True, "selftest": True, "op": req.get("op")})
        sys.exit(0)
    main()
