# llm-proxy

A minimal OpenAI API-compatible LLM proxy.  
OpenAI API 互換の最小機能の LLM プロキシです。

## 特徴
- Routes requests to LLM REST APIs (OpenAI API-compatible) defined in `config.json` based on the model name  
  モデル名を見て `config.json` に記述した LLM REST API (OpenAI API 互換) にリクエストを振り分ける
  - Aggregates multiple cloud LLM services and distributed local LLMs (across servers/processes) into a single endpoint  
    複数のクラウド LLM サービス、複数サーバやプロセスに分散したローカルLLM を 1 エンドポイントにまとめる
  - Allows tools like OpenClaw or AI agents to use cloud LLMs without exposing API keys  
    OpenClaw や AI エージェントに API キーを知らせずにクラウド LLM を利用させる
- Supports streaming responses / ストリーミングに対応
- Supports Cohere's Rerank API / Cohere の Rerank API にも対応
- Implemented using only the Go standard library, minimizing supply chain attack risks  
  Go 標準ライブラリのみで記述されており、サプライチェーン攻撃のリスクを最小限に

## Supported APIs / 対応 API
- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/embeddings`
- `POST /v1/rerank`
- `POST /v2/rerank`

## Usage / 使い方
```bash
cp config.example.json config.json
export OPENAI_API_KEY=...
export ANTHROPIC_API_KEY=...
go run . -config ./config.json
```


## Docker (multi-stage build)
```bash
docker build -t llm-proxy:local .
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=$OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  -v $(pwd)/config.json:/app/config.json:ro \
  llm-proxy:local
```
