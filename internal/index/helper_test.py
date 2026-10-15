"""Focused, dependency-free cache/session/write-order regression tests.

Run: uv run --no-project python -m unittest discover -s internal/index -p helper_test.py
Real MiniLM/Chroma compatibility and timing are separate integration probes.
"""
import copy
from contextlib import closing
from concurrent.futures import ThreadPoolExecutor
import threading
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("helper", Path(__file__).with_name("helper.py"))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


def embed(texts):
    # Text-dependent deterministic vectors, with the real model's dimension.
    return [[float(sum(t.encode("utf-8")) % 97) / 97] * 384 for t in texts]


class Collection:
    def __init__(self):
        self.rows = {}
        self.deleted = []
        self.writes = 0
        self.fail = False

    def get(self, where, include):
        return {"ids": [key for key, row in self.rows.items() if row["metadata"]["path"] == where["path"]]}

    def upsert(self, ids, documents, metadatas, embeddings):
        self.writes += 1
        for key, text, meta, vector in zip(ids, documents, metadatas, embeddings):
            self.rows[key] = {"text": text, "metadata": copy.deepcopy(meta), "vector": vector}
        if self.fail:
            raise RuntimeError("write failed after accepting rows")

    def delete(self, ids=None, where=None):
        keys = ids if ids is not None else [key for key, row in self.rows.items()
                                           if row["metadata"]["path"] in where["path"]["$in"]]
        for key in keys:
            self.deleted.append(key)
            self.rows.pop(key, None)

    def count(self):
        return len(self.rows)


def document(path, ordinal, text, revision=1):
    return {"id": f"{path}#{ordinal}", "text": text,
            "metadata": {"path": path, "ord": ordinal, "digest": str(revision),
                         "title": text.split("\n")[0], "heading": text.split("\n")[1]}}


class CacheTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = self.temp.name

    def cached(self, texts, model="model-v1", embedding=embed):
        return helper.cached_embeddings(self.directory, texts, embedding, model)

    def test_reuse_is_persistent_across_processes_and_duplicates(self):
        # Separate interpreters prove persistence, not a module-global memo.
        def run(texts):
            result = subprocess.run([sys.executable, __file__, "--cache-worker", self.directory,
                                     json.dumps(texts)], check=True, capture_output=True, text=True)
            return json.loads(result.stdout)
        first = run(["title\nheading\n\nfirst", "duplicate", "duplicate"])
        self.assertEqual(first[1:], [2, 1])
        second = run(["duplicate", "title\nheading\n\nfirst", "new"])
        self.assertEqual(second[1:], [1, 2])
        self.assertEqual(second[0][0], first[0][1])
        self.assertEqual(second[0][1], first[0][0])

    def test_model_version_and_exact_input_invalidate(self):
        self.assertEqual(self.cached(["Title\nHeading\n\nBody"])[1:], (1, 0))
        self.assertEqual(self.cached(["Title\nHeading\n\nBody"])[1:], (0, 1))
        for text in ["Other\nHeading\n\nBody", "Title\nOther\n\nBody", "Title\nHeading\n\nBody "]:
            self.assertEqual(self.cached([text])[1:], (1, 0))
        self.assertEqual(self.cached(["Title\nHeading\n\nBody"], model="model-v2")[1:], (1, 0))

    def test_failure_rolls_back_fill_and_retry_infers_again(self):
        def fail(texts):
            raise RuntimeError("inference unavailable")
        with self.assertRaisesRegex(RuntimeError, "inference unavailable"):
            self.cached(["a"], embedding=fail)
        self.assertEqual(self.cached(["a"])[1:], (1, 0))

    def test_corrupt_finite_vector_and_bad_shape_fail_explicitly(self):
        self.cached(["a"])
        path = Path(self.directory) / "embeddings.sqlite3"
        with closing(sqlite3.connect(path)) as db:
            db.execute("UPDATE embeddings SET vector=?", (json.dumps([0.5] * 384),))
            db.commit()
        with self.assertRaisesRegex(ValueError, "checksum"):
            self.cached(["a"])
        for values in ([1.0], [float("nan")] * 384, [float("inf")] * 384):
            with self.assertRaisesRegex(ValueError, "shape|non-finite"):
                helper.vector_json(values)
        with self.assertRaises(sqlite3.DatabaseError):
            path.write_bytes(b"corrupt database bytes")
            self.cached(["a"])

    def test_cache_private_even_with_permissive_umask(self):
        previous = os.umask(0)
        try:
            self.cached(["secret"])
        finally:
            os.umask(previous)
        self.assertEqual(Path(self.directory).stat().st_mode & 0o777, 0o700)
        self.assertEqual((Path(self.directory) / "embeddings.sqlite3").stat().st_mode & 0o777, 0o600)

    def test_concurrent_processes_commit_one_valid_row(self):
        args = [sys.executable, __file__, "--cache-worker", self.directory, '["same","same"]']
        children = [subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                    for _ in range(3)]
        results = []
        for child in children:
            stdout, stderr = child.communicate(timeout=10)
            self.assertEqual(child.returncode, 0, stderr)
            results.append(json.loads(stdout))
        inferred = sum(result[1] for result in results)
        self.assertTrue(1 <= inferred <= 3)  # concurrent cold misses may compute twice
        self.assertEqual(sum(result[2] for result in results), 6 - inferred)
        with closing(sqlite3.connect(Path(self.directory) / "embeddings.sqlite3")) as db:
            self.assertEqual(db.execute("SELECT count(*) FROM embeddings").fetchone()[0], 1)
        self.assertTrue(all(result[0] == results[0][0] for result in results))

    def test_cached_query_does_not_wait_for_blocked_inference(self):
        self.cached(["warm query"])
        entered, release = threading.Event(), threading.Event()
        def blocked(texts):
            entered.set()
            if not release.wait(5):
                raise RuntimeError("test inference was never released")
            return embed(texts)
        with ThreadPoolExecutor(max_workers=2) as pool:
            cold = pool.submit(self.cached, ["cold index chunk"], embedding=blocked)
            try:
                self.assertTrue(entered.wait(2))
                warm = pool.submit(self.cached, ["warm query"])
                self.assertEqual(warm.result(timeout=1)[1:], (0, 1))
            finally:
                release.set()
            self.assertEqual(cold.result(timeout=2)[1:], (1, 0))

    def test_replacements_keep_current_rows_and_delete_only_obsolete(self):
        col = Collection()
        original_cache = helper.cached_embeddings
        def cache(directory, texts):
            return original_cache(directory, texts, embed, "model-v1")
        def replace(texts, revision):
            docs = [document("a.md", i, text, revision) for i, text in enumerate(texts)]
            with patch.object(helper, "cached_embeddings", cache):
                result = helper.upsert(col, {"dir": self.directory, "delete_paths": ["a.md"],
                                             "replace_paths": ["a.md"], "docs": docs})
            self.assertEqual(set(col.rows), {d["id"] for d in docs})
            for doc in docs:
                self.assertEqual(col.rows[doc["id"]]["metadata"], doc["metadata"])
                self.assertEqual(col.rows[doc["id"]]["text"], doc["text"])
                self.assertEqual(col.rows[doc["id"]]["vector"], embed([doc["text"]])[0])
            return result
        a, b, c = "T\nH\n\na", "T\nH\n\nb", "T\nH\n\nc"
        self.assertEqual(replace([a, b, b], 1)["embedded"], 2)
        result = replace([a, b, b, c], 2)  # append
        self.assertEqual((result["embedded"], result["reused"]), (1, 3))
        self.assertEqual(col.deleted, [])
        result = replace([c, b, a], 3)  # reorder and remove duplicate
        self.assertEqual((result["embedded"], result["reused"]), (0, 3))
        self.assertEqual(col.deleted, ["a.md#3"])
        self.assertEqual(replace([a, "T\nH\n\nchanged"], 4)["embedded"], 1)
        self.assertEqual(replace(["New title\nH\n\na", "T\nNew heading\n\nchanged"], 5)["embedded"], 2)
        replace([], 6)  # empty note must remove all old rows
        self.assertEqual(col.count(), 0)
        replace([a], 7)
        helper.upsert(col, {"dir": self.directory, "delete_paths": ["a.md"], "docs": []})
        self.assertEqual(col.count(), 0)

    def test_inference_precedes_mutations_and_failed_upsert_retry_converges(self):
        col = Collection()
        docs = [document("a.md", 0, "T\nH\n\nbody")]
        req = {"dir": self.directory, "docs": docs, "delete_paths": ["a.md", "gone.md"]}
        col.rows["a.md#1"] = {"metadata": {"path": "a.md"}}
        col.rows["gone.md#0"] = {"metadata": {"path": "gone.md"}}
        before = copy.deepcopy(col.rows)
        with patch.object(helper, "cached_embeddings", side_effect=RuntimeError("inference")):
            with self.assertRaisesRegex(RuntimeError, "inference"):
                helper.upsert(col, req)
        self.assertEqual(col.rows, before)
        self.assertEqual(col.writes, 0)
        original_cache = helper.cached_embeddings
        with patch.object(helper, "cached_embeddings", lambda directory, texts:
                          original_cache(directory, texts, embed, "v1")):
            col.fail = True
            with self.assertRaisesRegex(RuntimeError, "write failed"):
                helper.upsert(col, req)
            self.assertEqual(col.deleted, [])
            col.fail = False
            result = helper.upsert(col, req)
        self.assertEqual(set(col.rows), {"a.md#0"})
        self.assertEqual((result["embedded"], result["reused"]), (0, 1))


# A real source-defined base lets identity tests hash the installed pipeline
# interface without requiring optional packages in this fast unit suite.
class FakeMiniLM:
    pass


class SessionTests(unittest.TestCase):
    module_name = "chromadb.utils.embedding_functions.onnx_mini_lm_l6_v2"

    def test_session_configuration_and_no_base_monkeypatch(self):
        class Options:
            def __init__(self):
                self.entries = {}
            def add_session_config_entry(self, key, value):
                self.entries[key] = value
        calls = []
        ort = types.SimpleNamespace(SessionOptions=Options,
                                    GraphOptimizationLevel=types.SimpleNamespace(ORT_ENABLE_ALL="all"),
                                    ExecutionMode=types.SimpleNamespace(ORT_SEQUENTIAL="sequential"),
                                    InferenceSession=lambda *args, **kwargs: calls.append((args, kwargs)))
        class Base:
            DOWNLOAD_PATH = "/scratch/model"
            EXTRACTED_FOLDER_NAME = "onnx"
            def __init__(self):
                self.ort = ort
        module = types.SimpleNamespace(ONNXMiniLM_L6_V2=Base)
        with patch.dict(sys.modules, {self.module_name: module}):
            embedding = helper.bounded_embedding_function()
            self.assertEqual(calls, [])  # construction stays lazy
            embedding.model
            embedding.model
        self.assertEqual(len(calls), 1)
        self.assertNotIn("model", Base.__dict__)
        args, kwargs = calls[0]
        self.assertEqual(args, ("/scratch/model/onnx/model.onnx",))
        self.assertEqual(kwargs["providers"], ["CPUExecutionProvider"])
        so = kwargs["sess_options"]
        self.assertEqual((so.intra_op_num_threads, so.inter_op_num_threads), (1, 1))
        self.assertEqual(so.execution_mode, "sequential")
        self.assertEqual(so.entries, {"session.intra_op.allow_spinning": "0", "session.inter_op.allow_spinning": "0"})

    def test_identity_follows_actual_artifacts_and_pipeline(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "onnx"
            root.mkdir()
            names = ("model.onnx", "tokenizer.json", "config.json", "special_tokens_map.json", "tokenizer_config.json", "vocab.txt")
            for name in names:
                (root / name).write_text(name)
            embedding = types.SimpleNamespace(DOWNLOAD_PATH=directory, EXTRACTED_FOLDER_NAME="onnx",
                                              _download_model_if_not_exists=lambda: None)
            module = types.SimpleNamespace(ONNXMiniLM_L6_V2=FakeMiniLM)
            with patch.dict(sys.modules, {self.module_name: module}), patch("importlib.metadata.version", return_value="1"):
                identity = helper.model_identity(embedding)
                self.assertEqual(helper.model_identity(embedding), identity)
                for name in names:
                    (root / name).write_text(name + "changed")
                    self.assertNotEqual(helper.model_identity(embedding), identity)
                    (root / name).write_text(name)
                with patch.object(helper, "PIPELINE_VERSION", "pipeline-changed"):
                    self.assertNotEqual(helper.model_identity(embedding), identity)
                with patch("importlib.metadata.version", return_value="2"):
                    self.assertNotEqual(helper.model_identity(embedding), identity)

    def test_empty_deletion_never_constructs_embedding_function(self):
        col = Collection()
        with patch.object(helper, "bounded_embedding_function", side_effect=AssertionError("model loaded")):
            result = helper.upsert(col, {"dir": "/unused", "delete_paths": ["gone.md"]})
        self.assertEqual(result["embedded"], 0)


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--cache-worker":
        print(json.dumps(helper.cached_embeddings(sys.argv[2], json.loads(sys.argv[3]), embed, "v1")))
    else:
        unittest.main()
