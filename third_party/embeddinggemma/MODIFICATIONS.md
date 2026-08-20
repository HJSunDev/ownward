# Model provenance and modifications

Ownward distributes `embeddinggemma-300m-qat-Q8_0.gguf`, SHA-256
`6fa0c02a9c302be6f977521d399b4de3a46310a4f2621ee0063747881b673f67`.

The file is the Q8_0 GGUF conversion published by `ggml-org` from Google's
EmbeddingGemma 300M model. Ownward has not modified that model file. Ownward
applies query/document prefixes, mean pooling, L2 normalization, and 512-dimension
truncation at runtime; these are runtime processing choices and do not modify the
distributed model file.

Source: https://huggingface.co/ggml-org/embeddinggemma-300m-qat-q8_0-GGUF
