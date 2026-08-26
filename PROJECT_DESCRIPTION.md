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

- OpenHands
- OpenClaw
- custom agent implementations
- future agent frameworks

Houdry should not depend on a single agent framework.

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

The preceding sections describe the long-term Houdry vision. The repository
currently implements only the Phase 1 GPU discovery and inventory-join slice:

- a Go CLI for Linux, macOS, and Windows build targets
- best-effort local GPU detection through available OS/vendor tools
- a normalized GPU inventory
- one-shot inventory submission to a basic HTTP server
- a shared-token option, JSON file storage, dashboard, and node listing
- server-hosted installers and cross-compiled binary distribution

The agentic workspace, agent execution, knowledge retrieval, model inference,
scheduling, remote execution, persistent node agents, and production security
described above are vision items and are not implemented in this repository.

The exact Phase 1 behavior and limitations are documented in
[`docs/GPU-discovery.md`](docs/GPU-discovery.md).
