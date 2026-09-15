# Model pair

Phase 1 uses two models from the same family: Qwen2.5 3B-Instruct as the fast tier
and Qwen2.5 7B-Instruct quantized to 4-bit (Q4) as the quality tier. Using one
family keeps the speed-versus-quality tradeoff genuine instead of comparing two
near-identical models.

The 3B model handles short, simple prompts with low latency. The 7B model at 4-bit
quantization handles longer or more complex prompts with better quality. Both fit
in 8GB of VRAM with a modest context window of 2 to 4 thousand tokens.

Only one chat model is resident on the GPU at a time. The router unloads the idle
model before loading the other. A different model family, such as Phi-4-mini, may
be added later to confirm the router is not hardcoded to one family's quirks.
