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
- Supports Anthropic's Messages API (`/v1/messages`) / Anthropic の Messages API にも対応
  - The API key in `api_key_env` is sent as the `x-api-key` header, and the client's `anthropic-version` / `anthropic-beta` headers are forwarded (`anthropic-version` defaults to `2023-06-01`)  
    `api_key_env` の API キーは `x-api-key` ヘッダで送信し、クライアントの `anthropic-version` / `anthropic-beta` ヘッダを転送します (`anthropic-version` 省略時は `2023-06-01`)
- `/v1/models` returns the model information reported by each upstream's `/v1/models` (such as `display_name`, `max_input_tokens`, `max_model_len`), limited to the models in `config.json`, in a shape both OpenAI and Anthropic clients can read. Results are cached for 5 minutes  
  `/v1/models` は各上流の `/v1/models` が返すモデル情報 (`display_name`、`max_input_tokens`、`max_model_len` など) を、`config.json` に書いたモデルに絞って返します。OpenAI と Anthropic のどちらのクライアントでも読める形式で、結果は 5 分間キャッシュします
  - The upstream format is detected from `base_url` (`api.anthropic.com` is Anthropic, others are OpenAI). Set `"api": "anthropic"` or `"api": "openai"` on a route to override it  
    上流の形式は `base_url` から判定します (`api.anthropic.com` は Anthropic、それ以外は OpenAI)。route に `"api": "anthropic"` / `"api": "openai"` を書くと上書きできます
- Implemented using only the Go standard library, minimizing supply chain attack risks  
  Go 標準ライブラリのみで記述されており、サプライチェーン攻撃のリスクを最小限に

## Supported APIs / 対応 API
- `GET /v1/models`
- `GET /v1/models/{model_id}`
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/embeddings`
- `POST /v1/rerank`
- `POST /v2/rerank`
- `POST /v1/messages` (Anthropic)
- `POST /v1/messages/count_tokens` (Anthropic)

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
