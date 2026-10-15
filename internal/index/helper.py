"""One JSON request per process; local Chroma and exact-input embedding cache.

The response uses a file because dependency downloaders may write to stdout.
Optional model dependencies stay lazy: stats, drop, deletion and selftest do
not load the tokenizer or create an inference session.
"""

import hashlib
import inspect
import json
import math
import os
import sqlite3
import sys
from functools import cached_property
from pathlib import Path


# These affect this helper only, never the daemon's job processes. Force the
# limits even when the caller has configured a larger BLAS/tokenizer pool.
for _key in ("OMP_NUM_THREADS", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS",
             "VECLIB_MAXIMUM_THREADS", "NUMEXPR_NUM_THREADS"):
    os.environ[_key] = "1"
os.environ["TOKENIZERS_PARALLELISM"] = "false"

PIPELINE_VERSION = "bermuda-minilm-exact-v1"
DIMENSIONS = 384


def respond(out, payload):
    with open(out, "w", encoding="utf-8") as fh:
        json.dump(payload, fh)


def bounded_embedding_function():
    """Extend only Chroma's session factory; retain its entire text pipeline."""
    from chromadb.utils.embedding_functions.onnx_mini_lm_l6_v2 import ONNXMiniLM_L6_V2

    class BoundedMiniLM(ONNXMiniLM_L6_V2):
        @cached_property
        def model(self):
            so = self.ort.SessionOptions()
            so.log_severity_level = 3
            so.graph_optimization_level = self.ort.GraphOptimizationLevel.ORT_ENABLE_ALL
            so.intra_op_num_threads = 1
            so.inter_op_num_threads = 1
            so.execution_mode = self.ort.ExecutionMode.ORT_SEQUENTIAL
            so.add_session_config_entry("session.intra_op.allow_spinning", "0")
            so.add_session_config_entry("session.inter_op.allow_spinning", "0")
            return self.ort.InferenceSession(
                os.path.join(self.DOWNLOAD_PATH, self.EXTRACTED_FOLDER_NAME, "model.onnx"),
                providers=["CPUExecutionProvider"], sess_options=so,
            )

    return BoundedMiniLM()


def model_identity(embedding):
    """Identify actual model/tokenizer bytes and the installed text pipeline.

    A package version or model name alone cannot notice a locally replaced
    model. Hash files once per helper call, before looking up cached vectors.
    Session thread policy is deliberately absent: it changes no text semantics.
    """
    from importlib.metadata import version
    from chromadb.utils.embedding_functions.onnx_mini_lm_l6_v2 import ONNXMiniLM_L6_V2

    embedding._download_model_if_not_exists()
    digest = hashlib.sha256()
    for value in (PIPELINE_VERSION, inspect.getsource(ONNXMiniLM_L6_V2),
                  *(version(p) for p in ("chromadb", "onnxruntime", "tokenizers", "numpy"))):
        digest.update(value.encode("utf-8"))
        digest.update(b"\0")
    folder = Path(embedding.DOWNLOAD_PATH) / embedding.EXTRACTED_FOLDER_NAME
    for name in ("model.onnx", "tokenizer.json", "config.json", "special_tokens_map.json",
                 "tokenizer_config.json", "vocab.txt"):
        digest.update(name.encode("ascii"))
        with (folder / name).open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
    return digest.hexdigest()


def vector_json(vector):
    vector = [float(value) for value in vector]
    if len(vector) != DIMENSIONS or not all(math.isfinite(value) for value in vector):
        raise ValueError("embedding cache: invalid vector shape or non-finite value")
    return json.dumps(vector, separators=(",", ":"), allow_nan=False)


def vector_checksum(identity, text, payload):
    # Length framing binds the vector to its exact model and input as well as
    # detecting plausible, finite bit/row corruption.
    digest = hashlib.sha256()
    for value in (identity, text, payload):
        data = value.encode("utf-8")
        digest.update(len(data).to_bytes(8, "big"))
        digest.update(data)
    return digest.hexdigest()


def cached_embeddings(directory, texts, embedding=None, identity=None):
    """Return vectors in input order, plus actual inferred/reused counts.

    Reads and inference never hold a writer lock, so cached queries remain
    responsive during a long fill. Only the final insert transaction is
    serialized; concurrent cold misses can infer twice. Corrupt rows fail
    explicitly, never as hits. No Chroma rows change before this returns.
    """
    if not texts:
        return [], 0, 0
    if embedding is None:
        embedding = bounded_embedding_function()
    if identity is None:
        identity = model_identity(embedding)
    directory = Path(directory)
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(directory, 0o700)
    path = directory / "embeddings.sqlite3"
    # SQLite sidecars inherit file/umask permissions. Keep the stricter umask
    # for the remainder of this short-lived helper, including Chroma writes.
    os.umask(0o077)
    fd = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
    os.close(fd)
    os.chmod(path, 0o600)
    db = sqlite3.connect(path, timeout=30)
    try:
        if not db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='embeddings'").fetchone():
            db.execute("""CREATE TABLE IF NOT EXISTS embeddings (
                model TEXT NOT NULL, input TEXT NOT NULL,
                vector TEXT NOT NULL, checksum TEXT NOT NULL,
                PRIMARY KEY (model, input))""")
        vectors = {}
        missing = []

        def stored(text):
            row = db.execute("SELECT vector, checksum FROM embeddings WHERE model=? AND input=?",
                             (identity, text)).fetchone()
            if row is None:
                return None
            payload, checksum = row
            if checksum != vector_checksum(identity, text, payload):
                raise ValueError("embedding cache: vector checksum mismatch")
            vector = json.loads(payload)
            vector_json(vector)  # shape/finiteness also required on cache hits
            return vector

        for text in dict.fromkeys(texts):
            vector = stored(text)
            if vector is None:
                missing.append(text)
            else:
                vectors[text] = vector
        for start in range(0, len(missing), 32):
            batch = missing[start:start + 32]
            inferred = embedding(batch)
            if len(inferred) != len(batch):
                raise ValueError("embedding cache: model returned wrong vector count")
            for text, vector in zip(batch, inferred):
                vectors[text] = json.loads(vector_json(vector))
        if missing:
            db.execute("BEGIN IMMEDIATE")
            for text in missing:
                payload = vector_json(vectors[text])
                db.execute("INSERT OR IGNORE INTO embeddings VALUES (?, ?, ?, ?)",
                           (identity, text, payload, vector_checksum(identity, text, payload)))
                # A concurrent filler may have won. Validate its row rather
                # than hiding corruption behind our freshly computed value.
                vectors[text] = stored(text)
            db.commit()
        return [vectors[text] for text in texts], len(missing), len(texts) - len(missing)
    finally:
        db.close()  # rolls back an incomplete commit


def collection(client, name):
    """Keep the existing collection/default EF identity and cosine geometry.

    All inference uses explicit vectors below; the collection's persisted
    default embedding configuration remains compatible with old helpers.
    """
    try:
        return client.get_or_create_collection(name, configuration={"hnsw": {"space": "cosine"}})
    except TypeError:
        return client.get_or_create_collection(name, metadata={"hnsw:space": "cosine"})


def upsert(col, req):
    docs = req.get("docs") or []
    paths = set(req.get("delete_paths") or [])
    # Explicit replacements include empty notes. Older callers supply only
    # delete_paths + docs, from which nonempty replacements can be inferred.
    replacements = set(req.get("replace_paths") or [])
    current = {}
    for doc in docs:
        path = doc["metadata"]["path"]
        replacements.add(path)
        current.setdefault(path, set()).add(doc["id"])
    obsolete = []
    for path in replacements:
        existing = col.get(where={"path": path}, include=[])["ids"]
        obsolete.extend(set(existing) - current.get(path, set()))

    # Finish all inference before mutating any persisted collection row.
    vectors, embedded, reused = cached_embeddings(req["dir"], [d["text"] for d in docs])
    for start in range(0, len(docs), 1000):
        batch = docs[start:start + 1000]
        col.upsert(ids=[d["id"] for d in batch],
                   documents=[d["text"] for d in batch],
                   metadatas=[d["metadata"] for d in batch],
                   embeddings=vectors[start:start + 1000])
    for start in range(0, len(obsolete), 1000):
        col.delete(ids=obsolete[start:start + 1000])
    removed = sorted(paths - replacements)
    for start in range(0, len(removed), 200):
        col.delete(where={"path": {"$in": removed[start:start + 200]}})
    return {"ok": True, "deleted_paths": len(paths), "upserted": len(docs),
            "embedded": embedded, "reused": reused, "count": col.count()}


def main():
    req = json.load(sys.stdin)
    out = req["out"]
    try:
        # Chroma and cache state are private even for a permissive caller.
        os.umask(0o077)
        os.makedirs(req["dir"], mode=0o700, exist_ok=True)
        os.chmod(req["dir"], 0o700)
        import chromadb
        from chromadb.config import Settings

        client = chromadb.PersistentClient(
            path=req["dir"], settings=Settings(anonymized_telemetry=False))
        op = req["op"]
        if op == "drop":
            try:
                client.delete_collection(req["collection"])
            except chromadb.errors.NotFoundError:
                pass
            respond(out, {"ok": True})
            return
        col = collection(client, req["collection"])
        if op == "stats":
            respond(out, {"ok": True, "count": col.count(), "chromadb": chromadb.__version__})
            return
        if op == "upsert":
            respond(out, upsert(col, req))
            return
        if op == "query":
            vectors, embedded, reused = cached_embeddings(req["dir"], [req["text"]])
            res = col.query(query_embeddings=vectors, n_results=int(req.get("n") or 8),
                            where=req.get("where") or None,
                            include=["documents", "metadatas", "distances"])
            hits = []
            ids = (res.get("ids") or [[]])[0]
            docs = (res.get("documents") or [[]])[0]
            metas = (res.get("metadatas") or [[]])[0]
            dists = (res.get("distances") or [[]])[0]
            for i in range(len(ids)):
                hits.append({"id": ids[i], "text": docs[i] if i < len(docs) else "",
                             "metadata": metas[i] if i < len(metas) else {},
                             "distance": dists[i] if i < len(dists) else None})
            respond(out, {"ok": True, "hits": hits, "embedded": embedded, "reused": reused})
            return
        respond(out, {"ok": False, "error": "unknown op %r" % op})
    except Exception as exc:
        respond(out, {"ok": False, "error": "%s: %s" % (type(exc).__name__, exc)})
        sys.exit(1)


if __name__ == "__main__":
    if os.environ.get("BERMUDA_INDEX_HELPER_SELFTEST"):
        req = json.load(sys.stdin)
        respond(req["out"], {"ok": True, "selftest": True, "op": req.get("op")})
        sys.exit(0)
    main()
