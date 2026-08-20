from __future__ import annotations

import json
import time
from pathlib import Path
from typing import Iterable, Iterator, Sequence

import numpy as np
import onnxruntime as ort
from transformers import AutoTokenizer


class OnnxTextEmbedder:
    def __init__(
        self,
        frozen_config: Path,
        model_key: str,
        variant: str,
        model_dir: Path,
        batch_size: int,
    ) -> None:
        config = json.loads(frozen_config.read_text(encoding="utf-8"))
        self.model_key = model_key
        self.variant = variant
        self.model_config = config["models"][model_key]
        self.variant_config = self.model_config[variant]
        self.model_dir = model_dir.resolve()
        self.batch_size = batch_size
        self.dimension = int(self.variant_config["dimension"])
        self.max_length = int(self.model_config["max_length"])
        self.tokenizer = AutoTokenizer.from_pretrained(str(self.model_dir), local_files_only=True)
        weight = self.model_dir / str(self.variant_config["weight"])
        if not weight.is_file():
            raise FileNotFoundError(weight)
        options = ort.SessionOptions()
        options.intra_op_num_threads = int(config["execution"]["intra_op_threads"])
        options.inter_op_num_threads = int(config["execution"]["inter_op_threads"])
        options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        self.session = ort.InferenceSession(
            str(weight),
            providers=["CPUExecutionProvider"],
            sess_options=options,
        )
        self.input_meta = {item.name: item for item in self.session.get_inputs()}
        self.output_names = {item.name for item in self.session.get_outputs()}
        if self.model_key == "embeddinggemma_300m":
            self.requested_output = "sentence_embedding"
        else:
            self.requested_output = "last_hidden_state"
        if self.requested_output not in self.output_names:
            raise RuntimeError(f"模型缺少输出：{self.requested_output}")

    def format_texts(self, texts: Sequence[str], prompt_type: str, instruction: str | None = None) -> list[str]:
        if prompt_type not in {"query", "document"}:
            raise ValueError(f"未知提示类型：{prompt_type}")
        if self.model_key == "qwen3_embedding_0_6b":
            if prompt_type == "document":
                prefix = str(self.model_config["document_prefix"])
            else:
                prefix = instruction or str(self.model_config["ownward_query_instruction"])
            return [prefix + text for text in texts]
        prefix = instruction if instruction is not None else str(self.model_config[f"{prompt_type}_prefix"])
        return [prefix + text for text in texts]

    def token_lengths(
        self,
        texts: Sequence[str],
        prompt_type: str,
        instruction: str | None = None,
        truncated: bool = False,
    ) -> list[int]:
        formatted = self.format_texts(texts, prompt_type, instruction)
        kwargs = {"add_special_tokens": True, "padding": False, "truncation": truncated}
        if truncated:
            kwargs["max_length"] = self.max_length
        encoded = self.tokenizer(formatted, **kwargs)
        return [len(item) for item in encoded["input_ids"]]

    def encode(
        self,
        texts: Sequence[str],
        prompt_type: str,
        instruction: str | None = None,
    ) -> np.ndarray:
        if not texts:
            return np.empty((0, self.dimension), dtype=np.float32)
        batches = []
        for start in range(0, len(texts), self.batch_size):
            batches.append(self._encode_batch(texts[start : start + self.batch_size], prompt_type, instruction))
        return np.concatenate(batches, axis=0)

    def encode_iter(
        self,
        texts: Iterable[str],
        prompt_type: str,
        instruction: str | None = None,
    ) -> Iterator[np.ndarray]:
        batch: list[str] = []
        for text in texts:
            batch.append(text)
            if len(batch) == self.batch_size:
                yield self._encode_batch(batch, prompt_type, instruction)
                batch = []
        if batch:
            yield self._encode_batch(batch, prompt_type, instruction)

    def timed_single(self, text: str, prompt_type: str = "query") -> tuple[np.ndarray, float]:
        start = time.perf_counter_ns()
        embedding = self._encode_batch([text], prompt_type, None)
        elapsed_ms = (time.perf_counter_ns() - start) / 1_000_000
        return embedding, elapsed_ms

    def _encode_batch(
        self,
        texts: Sequence[str],
        prompt_type: str,
        instruction: str | None,
    ) -> np.ndarray:
        formatted = self.format_texts(texts, prompt_type, instruction)
        encoded = self.tokenizer(
            formatted,
            padding=True,
            truncation=True,
            max_length=self.max_length,
            return_tensors="np",
        )
        feeds: dict[str, np.ndarray] = {}
        for name in ("input_ids", "attention_mask", "token_type_ids"):
            if name in self.input_meta and name in encoded:
                feeds[name] = np.asarray(encoded[name], dtype=np.int64)
        attention_mask = np.asarray(encoded["attention_mask"], dtype=np.int64)
        if "position_ids" in self.input_meta:
            feeds["position_ids"] = np.maximum(np.cumsum(attention_mask, axis=1) - 1, 0).astype(np.int64)
        for name, metadata in self.input_meta.items():
            if not name.startswith("past_key_values."):
                continue
            shape = []
            for index, value in enumerate(metadata.shape):
                if index == 0:
                    shape.append(len(texts))
                elif isinstance(value, int):
                    shape.append(value)
                elif "past" in str(value).lower() or "sequence" in str(value).lower():
                    shape.append(0)
                else:
                    raise RuntimeError(f"无法确定缓存输入形状：{name} {metadata.shape}")
            feeds[name] = np.empty(tuple(shape), dtype=np.float32)

        output = np.asarray(self.session.run([self.requested_output], feeds)[0], dtype=np.float32)
        if self.model_key == "embeddinggemma_300m":
            pooled = output
        elif self.model_key == "qwen3_embedding_0_6b":
            token_indices = np.broadcast_to(np.arange(attention_mask.shape[1]), attention_mask.shape)
            last_indices = np.where(attention_mask > 0, token_indices, -1).max(axis=1)
            pooled = output[np.arange(output.shape[0]), last_indices]
        else:
            pooled = output[:, 0]
        pooled = pooled[:, : self.dimension]
        norms = np.linalg.norm(pooled, axis=1, keepdims=True)
        if not np.isfinite(pooled).all() or np.any(norms <= 0):
            raise RuntimeError("模型产生了无效向量")
        return np.ascontiguousarray(pooled / norms, dtype=np.float32)
