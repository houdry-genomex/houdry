# Houdry

## Project Description

Houdry is a sovereign, modular AI execution platform designed to provide organizations with the capabilities of modern agentic AI systems while keeping computation, models, data, and execution within their own infrastructure.

Houdry combines a Codex-like agentic workspace with a distributed local compute fabric. It allows organizations to connect available laptops, workstations, GPU servers, and other compute resources into a managed private AI environment.

The platform is designed for environments where sensitive information cannot be sent to external AI services and where organizations still need modern AI capabilities such as coding, reasoning, multimodal understanding, document processing, tool execution, and autonomous multi-step workflows.

Houdry is not tied to a particular AI model, agent framework, GPU vendor, or execution runtime.

---

# The Problem

Modern AI development tools such as coding agents and multimodal assistants can perform complex tasks that previously required significant manual effort.

These systems can:

- understand large codebases
- read and modify files
- write and execute code
- analyze documents
- use external tools
- perform multi-step tasks
- reason over information
- generate real artifacts
- work iteratively instead of producing a single response

However, many of these systems rely on cloud-hosted models and infrastructure.

For organizations handling confidential information, sending internal information to external AI services can introduce significant concerns around:

- data confidentiality
- intellectual property
- regulatory requirements
- data sovereignty
- information leakage
- external dependency
- network isolation
- organizational control

At the same time, completely avoiding modern AI tools can reduce developer and organizational productivity.

Organizations therefore need a way to obtain modern agentic AI capabilities while keeping sensitive workloads inside their own infrastructure.

---

# The Houdry Vision

Houdry aims to provide a private alternative to cloud-dependent agentic AI infrastructure.

The vision is:

> Give an organization a powerful AI workstation without requiring the organization's data or computation to leave its infrastructure.

A user should be able to interact with Houdry in a way that feels similar to modern AI development environments.

The user can provide a task such as:

> Analyze this inspection report, inspect the attached engineering drawing, search the internal safety procedures, calculate the required values from the spreadsheet, verify the calculation, and generate an approval note.

Houdry should be capable of orchestrating the required models, tools, compute resources, and execution environments to complete the task.

---

# What Houdry Provides

Houdry consists of several major capabilities.

## 1. Agentic AI Workspace

Houdry provides a desktop-first AI workspace inspired by modern coding agents.

The workspace allows users to:

- interact with an AI agent
- browse files
- edit files
- inspect documents
- view images and engineering drawings
- work with code
- execute tasks
- inspect agent activity
- review generated artifacts
- monitor system resources
- inspect execution history

The interface should feel like an AI-powered engineering workstation rather than a traditional chatbot.

---

# 2. Agent Execution

Houdry provides an environment in which an AI agent can perform multi-step work.

An agent may:

1. Understand the user's objective.
2. Break the task into steps.
3. Inspect relevant files.
4. Search available knowledge.
5. Select appropriate tools.
6. Generate code.
7. Execute code in a controlled environment.
8. Observe the result.
9. Iterate when necessary.
10. Verify the output.
11. Produce the requested artifact.

The underlying agent implementation is intentionally replaceable.

Houdry may integrate with existing agent frameworks or custom agents.

Examples may include:

- **Houdry Agent** ([houdry-genomex/houdry-agent](https://github.com/houdry-genomex/houdry-agent)) — recommended Desktop client; a branded fork of Hermes Agent (Nous Research, MIT) that calls this fabric’s `/v1` API
- OpenHands
- OpenClaw
- custom agent implementations
- future agent frameworks

Houdry should not depend on a single agent framework. The **fabric** (this
repository) is for GPU/ops hosts; **Houdry Agent** is for normal users.

---

# 3. Distributed Local Compute

Houdry can turn available organizational compute into a unified private compute fabric.

A node may be:

- a laptop
- a workstation
- a GPU server
- a CPU server
- another supported compute machine

A node can join the Houdry environment through a simple installation and registration process.

For example:

```bash
houdry gpu join --server http://houdry-server:8080
```

The command above describes the implemented Phase 1 GPU-inventory join. It
sends a one-time snapshot; it does not yet create a persistent worker, execute
remote jobs, or make the GPU schedulable.

---

# Current Implementation Status

The preceding sections describe the long-term Houdry vision.

**Phase 1 (implemented):** local GPU discovery, one-shot inventory join, control
plane listing/dashboard, cross-platform installers.

**Phase 2 (implemented prototype):** persistent node agent (`houdry node join`),
heartbeat with READY/BUSY/OFFLINE, job submit/claim/result, and `gpu.smoke`
execution over the same HTTP APIs used for local and remote nodes.

**Phase 3 (implemented prototype):** multi-node registration, normalized
static/dynamic resource profiles (physical GPUs ≠ runtime probes),
framework-agnostic workload requirements, first-fit VRAM-aware scheduler,
job queue, DRAINING/leave, offline exclusion, and cluster visibility
(`houdry node list`, dashboard, `/v1/cluster`).

**Phase 5 (implemented prototype):** intelligent model & resource routing —
heuristic task profiles (modality/complexity), runtime-agnostic model catalog,
scoring of (model, node) pairs (prefer loaded + complexity-appropriate models),
`POST /v1/route`, and `houdry route --prompt … [--execute]`. Vision/OCR/PDF
pipelines are detected and deferred.

**OpenAI-compatible API (additive):** `POST /v1/chat/completions` and
`GET /v1/models` so OpenHands (and other OpenAI SDK clients) can use Houdry as
an LLM backend. `model=auto` reuses the Phase 5 router; inference still runs as
a Houdry job on a node agent via the Model Runtime API (not Ollama-hardcoded).

Still not implemented: agentic workspace, RAG, OCR/vision chains, embedding
OpenHands inside Houdry, and durable multi-tenant security.

Exact contracts:
- [`docs/GPU-discovery.md`](docs/GPU-discovery.md)
- [`docs/Phase-2-node-agent.md`](docs/Phase-2-node-agent.md)
- [`docs/Phase-3-scheduling.md`](docs/Phase-3-scheduling.md)
- [`docs/Phase-4-model-runtime.md`](docs/Phase-4-model-runtime.md)
- [`docs/Phase-5-routing.md`](docs/Phase-5-routing.md)
- [`docs/OpenAI-compatible-API.md`](docs/OpenAI-compatible-API.md)
