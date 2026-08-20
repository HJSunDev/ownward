import fs from "node:fs";
import path from "node:path";
import readline from "node:readline";
import { performance } from "node:perf_hooks";
import ort from "../delivery-runtime-v3/node_modules/onnxruntime-node/dist/index.js";
import { AutoTokenizer, env } from "../delivery-runtime-v3/node_modules/@huggingface/transformers/dist/transformers.node.mjs";


const [configPath, modelKey, modelDir, sessionConfigPath] = process.argv.slice(2);
if (!configPath || !modelKey || !modelDir || !sessionConfigPath) {
  throw new Error("缺少 configPath、modelKey、modelDir 或 sessionConfigPath");
}
if (modelKey !== "embeddinggemma_300m") {
  throw new Error("该工作进程只允许验证 EmbeddingGemma");
}

env.allowRemoteModels = false;
env.allowLocalModels = true;
env.localModelPath = modelDir;
env.useBrowserCache = false;

const config = JSON.parse(fs.readFileSync(configPath, "utf8"));
const modelConfig = config.models[modelKey];
const sessionConfig = JSON.parse(fs.readFileSync(sessionConfigPath, "utf8"));
const sessionOptions = {
  executionProviders: ["cpu"],
  executionMode: "sequential",
  graphOptimizationLevel: "all",
  intraOpNumThreads: sessionConfig.intra_op_threads,
  interOpNumThreads: 1,
  enableCpuMemArena: sessionConfig.enable_cpu_mem_arena,
  enableMemPattern: sessionConfig.enable_mem_pattern,
};

const started = performance.now();
const tokenizer = await AutoTokenizer.from_pretrained(modelDir, { local_files_only: true });
const session = await ort.InferenceSession.create(
  path.join(modelDir, "onnx", "model_quantized.onnx"),
  sessionOptions,
);
write({
  type: "ready",
  model_key: modelKey,
  session_config: sessionConfig,
  load_ms: performance.now() - started,
});

const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
for await (const line of lines) {
  if (!line.trim()) continue;
  const request = JSON.parse(line);
  if (request.command === "shutdown") {
    write({ type: "stopped" });
    break;
  }
  const requestStarted = performance.now();
  const formatted = modelConfig[`${request.prompt_type ?? "query"}_prefix`] + String(request.text);
  const inputs = await tokenizer(formatted, {
    padding: false,
    truncation: true,
    max_length: modelConfig.max_length,
  });
  const feeds = {
    input_ids: new ort.Tensor("int64", inputs.input_ids.data, inputs.input_ids.dims),
    attention_mask: new ort.Tensor("int64", inputs.attention_mask.data, inputs.attention_mask.dims),
  };
  const outputs = await session.run(feeds);
  const vector = normalizedVector(outputs.sentence_embedding.data, modelConfig.deliverable.dimension);
  const response = {
    id: request.id,
    dimension: vector.length,
    checksum: vector.reduce((sum, value, index) => sum + value * ((index % 17) + 1), 0),
    token_count: Number(inputs.input_ids.dims.at(-1)),
    elapsed_ms: performance.now() - requestStarted,
  };
  if (request.return_vector) response.vector = Array.from(vector);
  write(response);
}

await session.release();
process.exit(0);


function normalizedVector(source, dimension) {
  const result = new Float32Array(dimension);
  let norm = 0;
  for (let index = 0; index < dimension; index += 1) {
    const value = Number(source[index]);
    result[index] = value;
    norm += value * value;
  }
  norm = Math.sqrt(norm);
  if (!Number.isFinite(norm) || norm === 0) throw new Error("模型产生了无效向量");
  for (let index = 0; index < dimension; index += 1) result[index] /= norm;
  return result;
}


function write(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}
