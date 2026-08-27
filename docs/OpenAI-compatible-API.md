# OpenAI-compatible API (OpenHands / external clients)

This is an **additive compatibility layer**. It does not replace Houdry’s native
APIs, router, scheduler, or model runtimes.

```text
OpenHands (or any OpenAI SDK client)
        ↓
POST /v1/chat/completions
        ↓
Houdry task router (model=auto)  OR  explicit model + node Fits()
        ↓
Inference job → node agent → Model Runtime API → Ollama/vLLM/…
        ↓
OpenAI-shaped JSON response
```

OpenHands is only a **client**. It is not embedded in Houdry.

## Why this exists

Many agent tools (including OpenHands) speak the OpenAI Chat Completions protocol.
Exposing that shape lets them use Houdry’s private GPU fabric without learning
Houdry-native job APIs — while still going through Houdry routing and scheduling.

## Enable / disable

Enabled by default on `houdry serve`.

```bash
houdry serve --listen 0.0.0.0:18080
# disable:
houdry serve --listen 0.0.0.0:18080 --no-openai-compat
# wait budget for sync completions:
houdry serve --openai-wait 15m
```

Localhost needs no auth when the server has no `--token`. If you set a token,
send `Authorization: Bearer <token>` or `X-Houdry-Token`.

## Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/v1/chat/completions` | Chat Completions (sync; optional SSE after completion) |
| GET | `/v1/models` | Lists `auto` + catalog refs |

Native Houdry routes (`/v1/nodes`, `/v1/jobs`, `/v1/route`, …) remain unchanged.

## Request

```json
{
  "model": "auto",
  "messages": [
    { "role": "user", "content": "Say hello from Houdry." }
  ],
  "temperature": 0.2,
  "max_tokens": 128,
  "stream": false
}
```

### Tools (OpenHands / agents)

`tools` and `tool_choice` are accepted and forwarded through the inference job
to the selected model runtime (Ollama `/api/chat`). Structured `tool_calls` in
the runtime response are returned in OpenAI form:

```json
{
  "choices": [{
    "finish_reason": "tool_calls",
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "call_0",
        "type": "function",
        "function": {
          "name": "execute_bash",
          "arguments": "{\"command\":\"ls\"}"
        }
      }]
    }
  }]
}
```

Notes:
- The router still only uses the last user text for model selection; it does
  not strip tools.
- Small models (e.g. `qwen2.5-coder:1.5b`) may emit tool intent as JSON text;
  Houdry normalizes recognizable tool JSON into `tool_calls` when `tools` were
  requested. Reliability is still model-dependent — prefer a tool-tuned model
  for production agents.

### `model`

| Value | Behavior |
|-------|----------|
| `auto` (or empty / `houdry`) | Existing Phase 5 router: modality → complexity → catalog → node |
| `tinyllama:latest` (etc.) | Explicit model; Houdry still picks a READY node via `Fits()` / scheduler |

Do **not** bypass the router by calling Ollama from this handler.

## Response (non-streaming)

```json
{
  "id": "chatcmpl-…",
  "object": "chat.completion",
  "created": 1710000000,
  "model": "tinyllama:latest",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "…" },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 11,
    "completion_tokens": 7,
    "total_tokens": 18
  }
}
```

## Streaming

`stream: true` is supported as **post-completion SSE**: Houdry still runs the
full inference job through the agent/runtime, then emits OpenAI-style chunks.
True token-by-token runtime streaming is a later enhancement.

## Example curl

```bash
# Terminal 1
houdry serve --listen 0.0.0.0:18080

# Terminal 2 — node agent with GPU + model runtime
houdry node join --server http://127.0.0.1:18080

# Terminal 3
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [
      { "role": "user", "content": "Say hello from Houdry." }
    ]
  }'
```

## Connecting OpenHands

Point the OpenHands LLM base URL at Houdry:

```text
OPENAI_API_BASE=http://127.0.0.1:18080/v1
OPENAI_API_KEY=houdry   # any non-empty value if the server has no token;
                        # otherwise use the Houdry serve --token value
```

Set the model to `auto` (recommended) or a concrete catalog ref such as
`tinyllama:latest`.

Exact OpenHands env var names may vary by version — use whatever that release
expects for an OpenAI-compatible endpoint.

## Errors

OpenAI-style `{ "error": { "message", "type", "code" } }` for invalid messages,
unavailable model, no READY GPU/node, timeouts, and runtime failures. Responses
avoid leaking node filesystem paths or credentials.
