<div align="center">

  <h1 align="center">
    MemDB 2.0: Stardust
    <img src="https://img.shields.io/badge/status-Preview-blue" alt="Preview Badge"/>
  </h1>

  <p>
    <a href="https://github.com/anatolykoptev/memdb">
      <img alt="Static Badge" src="https://img.shields.io/badge/Maintained_by-MemDB-blue">
    </a>
    <a href="https://pypi.org/project/memdb">
      <img src="https://img.shields.io/pypi/v/memdb?label=pypi-not-yet-released" alt="PyPI Version">
    </a>
    <a href="https://pypi.org/project/memdb">
      <img src="https://img.shields.io/pypi/pyversions/memdb.svg" alt="Supported Python versions">
    </a>
    <a href="https://pypi.org/project/memdb">
      <img src="https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey" alt="Supported Platforms">
    </a>
    <a href="https://arxiv.org/abs/2507.03724">
      <img src="https://img.shields.io/badge/arXiv-2507.03724-b31b1b.svg" alt="Based on MemOS Research">
    </a>
    <a href="https://github.com/anatolykoptev/memdb/discussions">
      <img src="https://img.shields.io/badge/GitHub-Discussions-181717.svg?logo=github" alt="GitHub Discussions">
    </a>
    <a href="https://discord.gg/Txbx3gebZR">
      <img src="https://img.shields.io/badge/Discord-join%20chat-7289DA.svg?logo=discord" alt="Discord">
    </a>
    <a href="https://opensource.org/license/apache-2-0/">
      <img src="https://img.shields.io/badge/License-Apache_2.0-green.svg?logo=apache" alt="License">
    </a>
    <a href="https://github.com/IAAR-Shanghai/Awesome-AI-Memory">
      <img alt="Awesome AI Memory" src="https://img.shields.io/badge/Resources-Awesome--AI--Memory-8A2BE2">
    </a>
  </p>

<p align="center">
  <strong>+43.70% Accuracy vs. OpenAI Memory</strong><br/>
  <strong>Top-tier long-term memory + personalization</strong><br/>
  <strong>Saves 35.24% memory tokens</strong><br/>
  <sub>LoCoMo 75.80 | LongMemEval +40.43% | PrefEval-10 +2568% | PersonaMem +40.75%</sub>
</p>

</div>

---

<br>

## MemDB: Memory Database for AI Agents

**MemDB** is a Memory Database for LLMs and AI agents that unifies **store / retrieve / manage** for long-term memory, enabling **context-aware and personalized** interactions with **KB**, **multi-modal**, **tool memory**, and **enterprise-grade** optimizations built in.

MemDB is a hard fork of [MemOS](https://github.com/MemTensor/MemOS) (arxiv 2507.03724), repackaged and rebranded for independent development.

### Key Features

- **Unified Memory API**: A single API to add, retrieve, edit, and delete memory -- structured as a graph, inspectable and editable by design, not a black-box embedding store.
- **Multi-Modal Memory**: Natively supports text, images, tool traces, and personas, retrieved and reasoned together in one memory system.
- **Multi-Cube Knowledge Base Management**: Manage multiple knowledge bases as composable memory cubes, enabling isolation, controlled sharing, and dynamic composition across users, projects, and agents.
- **Asynchronous Ingestion via MemScheduler**: Run memory operations asynchronously with millisecond-level latency for production stability under high concurrency.
- **Memory Feedback & Correction**: Refine memory with natural-language feedback -- correcting, supplementing, or replacing existing memories over time.


### Changelog

- **2025-12-24** -- **MemDB v2.0: Stardust Release**
  Comprehensive KB (doc/URL parsing + cross-project sharing), memory feedback & precise deletion, multi-modal memory (images/charts), tool memory for agent planning, Redis Streams scheduling + DB optimizations, streaming/non-streaming chat, MCP upgrade, and lightweight quick/full deployment.
  <details>
    <summary><b>New Features</b></summary>

  **Knowledge Base & Memory**
  - Added knowledge base support for long-term memory from documents and URLs

  **Feedback & Memory Management**
  - Added natural language feedback and correction for memories
  - Added memory deletion API by memory ID
  - Added MCP support for memory deletion and feedback

  **Conversation & Retrieval**
  - Added chat API with memory-aware retrieval
  - Added memory filtering with custom tags (Cloud & Open Source)

  **Multimodal & Tool Memory**
  - Added tool memory for tool usage history
  - Added image memory support for conversations and documents

  </details>

  <details>
    <summary><b>Improvements</b></summary>

  **Data & Infrastructure**
  - Upgraded database for better stability and performance

  **Scheduler**
  - Rebuilt task scheduler with Redis Streams and queue isolation
  - Added task priority, auto-recovery, and quota-based scheduling

  **Deployment & Engineering**
  - Added lightweight deployment with quick and full modes

  </details>

  <details>
    <summary><b>Bug Fixes</b></summary>

  **Memory Scheduling & Updates**
  - Fixed legacy scheduling API to ensure correct memory isolation
  - Fixed memory update logging to show new memories correctly

  </details>

- **2025-08-07** -- **v1.0.0 (MemCube) Release**
  First MemCube release with a word-game demo, LongMemEval evaluation, BochaAISearchRetriever integration, NebulaGraph support, improved search capabilities, and the official Playground launch.

- **2025-07-07** -- **v1.0: Stellar Preview Release**
  A SOTA Memory system for LLMs is now open-sourced.
- **2025-07-04** -- **Paper Release**
  [MemOS: A Memory OS for AI System](https://arxiv.org/abs/2507.03724) is available on arXiv.

<br>

## Quickstart Guide

### Self-Hosted (Local/Private)

1. Get the repository.
    ```bash
    git clone https://github.com/anatolykoptev/memdb.git
    cd MemDB
    pip install -r ./docker/requirements.txt
    ```
2. Configure `docker/.env.example` and copy to `MemDB/.env`
3. Start the service.

- Launch via Docker
  ```bash
  cd docker
  docker compose up
  ```

- Launch via the uvicorn command line interface (CLI)
  Make sure that your graph DB and vector DB are running before executing:
  ```bash
  cd src
  uvicorn memdb.api.server_api:app --host 0.0.0.0 --port 8001 --workers 1
  ```

### Basic Usage (Self-Hosted)
  - Add User Message
    ```python
    import requests
    import json

    data = {
        "user_id": "8736b16e-1d20-4163-980b-a5063c3facdc",
        "mem_cube_id": "b32d0977-435d-4828-a86f-4f47f8b55bca",
        "messages": [
            {
                "role": "user",
                "content": "I like strawberry"
            }
        ],
        "async_mode": "sync"
    }
    headers = {
        "Content-Type": "application/json"
    }
    url = "http://localhost:8000/product/add"

    res = requests.post(url=url, headers=headers, data=json.dumps(data))
    print(f"result: {res.json()}")
    ```
  - Search User Memory
    ```python
    import requests
    import json

    data = {
        "query": "What do I like",
        "user_id": "8736b16e-1d20-4163-980b-a5063c3facdc",
        "mem_cube_id": "b32d0977-435d-4828-a86f-4f47f8b55bca"
    }
    headers = {
        "Content-Type": "application/json"
    }
    url = "http://localhost:8000/product/search"

    res = requests.post(url=url, headers=headers, data=json.dumps(data))
    print(f"result: {res.json()}")
    ```

<br>

## Resources

- **Awesome-AI-Memory**
 A curated repository dedicated to resources on memory and memory systems for large language models. It systematically collects relevant research papers, frameworks, tools, and practical insights.
- **Get started**: [IAAR-Shanghai/Awesome-AI-Memory](https://github.com/IAAR-Shanghai/Awesome-AI-Memory)

<br>

## Community & Support

Join our community to ask questions, share your projects, and connect with other developers.

- **GitHub Issues**: Report bugs or request features in our <a href="https://github.com/anatolykoptev/memdb/issues" target="_blank">GitHub Issues</a>.
- **GitHub Pull Requests**: Contribute code improvements via <a href="https://github.com/anatolykoptev/memdb/pulls" target="_blank">Pull Requests</a>.
- **GitHub Discussions**: Participate in our <a href="https://github.com/anatolykoptev/memdb/discussions" target="_blank">GitHub Discussions</a> to ask questions or share ideas.
- **Discord**: Join our <a href="https://discord.gg/Txbx3gebZR" target="_blank">Discord Server</a>.

<br>

## Citation

If you use MemDB in your research, we would appreciate citations to the original papers.

```bibtex

@article{li2025memos_long,
  title={MemOS: A Memory OS for AI System},
  author={Li, Zhiyu and Song, Shichao and Xi, Chenyang and Wang, Hanyu and Tang, Chen and Niu, Simin and Chen, Ding and Yang, Jiawei and Li, Chunyu and Yu, Qingchen and Zhao, Jihao and Wang, Yezhaohui and Liu, Peng and Lin, Zehao and Wang, Pengyuan and Huo, Jiahao and Chen, Tianyi and Chen, Kai and Li, Kehang and Tao, Zhen and Ren, Junpeng and Lai, Huayi and Wu, Hao and Tang, Bo and Wang, Zhenren and Fan, Zhaoxin and Zhang, Ningyu and Zhang, Linfeng and Yan, Junchi and Yang, Mingchuan and Xu, Tong and Xu, Wei and Chen, Huajun and Wang, Haofeng and Yang, Hongkang and Zhang, Wentao and Xu, Zhi-Qin John and Chen, Siheng and Xiong, Feiyu},
  journal={arXiv preprint arXiv:2507.03724},
  year={2025},
  url={https://arxiv.org/abs/2507.03724}
}

@article{li2025memos_short,
  title={MemOS: An Operating System for Memory-Augmented Generation (MAG) in Large Language Models},
  author={Li, Zhiyu and Song, Shichao and Wang, Hanyu and Niu, Simin and Chen, Ding and Yang, Jiawei and Xi, Chenyang and Lai, Huayi and Zhao, Jihao and Wang, Yezhaohui and others},
  journal={arXiv preprint arXiv:2505.22101},
  year={2025},
  url={https://arxiv.org/abs/2505.22101}
}

@article{yang2024memory3,
author = {Yang, Hongkang and Zehao, Lin and Wenjin, Wang and Wu, Hao and Zhiyu, Li and Tang, Bo and Wenqiang, Wei and Wang, Jinbo and Zeyun, Tang and Song, Shichao and Xi, Chenyang and Yu, Yu and Kai, Chen and Xiong, Feiyu and Tang, Linpeng and Weinan, E},
title = {Memory$^3$: Language Modeling with Explicit Memory},
journal = {Journal of Machine Learning},
year = {2024},
volume = {3},
number = {3},
pages = {300--346},
issn = {2790-2048},
doi = {https://doi.org/10.4208/jml.240708},
url = {https://global-sci.com/article/91443/memory3-language-modeling-with-explicit-memory}
}
```

<br>

## Contributing

We welcome contributions from the community! Please read our [contribution guidelines](https://github.com/anatolykoptev/memdb/blob/main/CONTRIBUTING.md) to get started.

<br>

## License

MemDB is licensed under the [Apache 2.0 License](./LICENSE).
