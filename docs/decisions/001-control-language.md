# Decision 001: Go for the local control program

**Status:** Accepted design following the 11 September 2026 constraint review; implementation and performance validation are incomplete.

**Date:** 2026-09-11.

**Decision owner:** Project maintainer.

This record explains why we chose Go for the program behind `./lab`. The choice follows the work it must do and how we want to install it. Installed language tools are not a design requirement.

The [researched constraints and language comparison](../constraints-and-language-choice.md) expands the alternatives to Rust, C, C++, Java/Kotlin, C#/.NET, Python and TypeScript. Its workload, failure assumptions and test targets govern this decision. Go is a defensible fit for process management and uploads, not a requirement imposed by the conflict reports.

## The work we need to do

The control program starts, watches and stops two shared video services and, for each registered source, a recorder, a forwarder and an optional generated test pattern. With the current four-source limit, this is at most fourteen child processes. Four real sources normally need ten: the two services plus four recorders and four forwarders. This limit is an implementation choice, not measured host capacity.

It needs to:

- Watch these programs at the same time, without one stalled connection stopping all other checks.
- Keep operating through a connection outage with no known end time. Individual checks and stop requests must still have time limits.
- Stop waiting work deliberately and shut down the child programs it owns. This is called cancellation.
- Save separate source credentials and settings, and track each completed recording's source, checksum and upload progress.
- Limit stored data and avoid retry loops that use increasing memory or constantly restart failing programs.
- Be testable when child programs crash, ignore a stop request or lose their input, including whether other sources continue working.
- Run on the Mac first, with a path to Linux later.

We prefer shipping one built control program for each operating system and processor type. The receiving machine should not need a separately installed language interpreter—the program that reads and runs source code. FFmpeg, FFprobe and MediaMTX remain separate video tools. Connection instructions are an embedded text template filled with each source's current settings; a deployed executable does not need that template separately on disk.

There is no requirement here that makes Go the only possible language. Python, Go, and Java can all handle this workload. A handful of child programs is not, by itself, a demanding concurrency problem. Concurrency means allowing several tasks to make progress during the same period.

## Decision

Use Go for the control program and recording uploader. Its standard library supplies file access, JSON, checksums, networking, process control, testing and cancellation. Additional dependencies are justified where they meet a real requirement: SQLite for durable progress and a suitable client for the R2 API. A standard-library-only rule does not take precedence over correct recovery.

Go offers a direct way to organize independent waiting tasks and produce an executable for each target. Its `context` package carries stop requests and time limits, while `os/exec` starts and waits for child programs. These tools support our design; they do not automatically make shutdown correct. We must write and test graceful stopping, time limits, and cleanup. [Go cancellation tools](https://pkg.go.dev/context), [Go process tools](https://pkg.go.dev/os/exec).

Developers need Go tools to build the source. A deployed control executable runs without those development tools. Go's support code, including memory management, is part of the built program. [Go runtime explanation](https://go.dev/doc/faq#runtime).

## Options considered

| Concern | Go | Python | Java |
| --- | --- | --- | --- |
| Waiting for several programs and connections | Small independent tasks, called goroutines, can wait and continue | `asyncio` supports concurrent network and subprocess work | Threads, including virtual threads, support concurrent waiting |
| Stopping work | Explicit cancellation and process handling; must be tested | Task cancellation and process handling; must be tested | Interrupts and process handling; must be tested |
| Installation | Build a control executable for each operating system and processor type | Supply a compatible Python interpreter, separately or in a tested application bundle | Supply a Java runtime, separately or in a bundle made with tools such as `jlink` |
| Testing | Built-in tests and tools to detect unsafe access to shared memory | Standard testing tools and straightforward process tests | Standard process tools and mature testing options |
| Maintenance | Check types when building; keep error paths explicit | Concise code; runtime checks and optional type checking need a clear policy | Check types when building; keep the application and build setup small |
| Extra framework required | None for this workload | None for this workload | None for this workload |

Python's `asyncio` supports input/output work where the program waits for network data or subprocesses. Python is a viable choice for this controller; we do not reject it on the claim that it cannot do concurrent work. [Python asyncio documentation](https://docs.python.org/3/library/asyncio.html).

Java's virtual threads make it practical to organize many tasks that spend time waiting. Java manages these tasks without needing a separate operating-system thread for each one. They do not make CPU-heavy work faster. `jlink` can assemble a runtime bundle: the application can be shipped with the Java support software it needs. A Java implementation would not require a web framework. [Java virtual threads](https://docs.oracle.com/en/java/javase/25/core/virtual-threads.html), [Java runtime packaging](https://docs.oracle.com/en/java/javase/25/docs/specs/man/jlink.html).

For this project, Go's process-control tools and per-target executable fit the deployment preference with relatively little packaging work. A team already strong in Python or Java could reasonably make a different choice. We have not measured a performance winner among three implementations.

## What this choice does not provide

FFmpeg and MediaMTX handle the actual video. Moving the controller to Go does not preserve more camera detail, create a better compression method, or make FFmpeg encode faster.

Go uses garbage collection: the program automatically finds and reclaims memory it no longer needs. That work, operating-system scheduling, and network behavior prevent us from promising that every action finishes by an absolute deadline. This is not a hard real-time system. Hard real time means every required deadline must be met. [Go runtime explanation](https://go.dev/doc/faq#runtime).

One executable also does not mean one build works everywhere. We must build and test each supported Mac/Linux and processor combination. A successful build for Linux is not proof that Linux process cleanup or network behavior has been tested.

## Durable progress uses SQLite

The Go code uses SQLite because completed recordings, interrupted uploads and safe retries need durable, controlled state changes. This is a storage decision independent of the language choice. JSON remains suitable for settings and replaceable status snapshots.

SQLite does not combine a local video file and an R2 object into one atomic change. Restart recovery reconciles completed files, database records and upload results. Stable recording identities make repeated attempts safe. Correct flushing and actual failure testing remain necessary. Local catalog and archive tests exist, and one generated recording was uploaded and confirmed in a real private R2 bucket. Network-interruption and physical power-loss tests remain separate validation steps. See the recovery explanation in the [current design decision](../constraints-and-language-choice.md).

## Consequences and checks

We gain a clear deployment artifact and a direct way to manage independently waiting tasks. We take on a build step, one release for each target, and the responsibility to test process behavior on each operating system. Go's type checks do not catch incorrect recovery logic.

Before relying on the controller, check that:

- A failed forwarder does not stop its local recording or viewing, or another source's forwarding.
- Different sources retain separate credentials, viewer paths, controls and recording identities.
- A source can remain absent while status and stop commands still respond.
- Repeated failures do not create unlimited child processes, memory use, or retries without pauses.
- Shutdown waits a limited time, cleans up owned processes, and reports failures.
- Interrupted file and database writes leave readable state or a clear recovery path; interrupted uploads resume without losing or duplicating recordings.
- The built control program starts on a machine without Go development tools, with its required video tools supplied.

Revisit the decision using measured CPU and memory use on the intended nearby computer, packaging tests, code maintenance effort, and the team's skills. If compression dominates CPU use, changing the controller language is unlikely to address that load.

This follows DDIA's approach: begin with required behavior, compare costs, then test the claims. The book guides the questions; it does not recommend Go for this project. [DDIA overview](https://dataintensive.net/).
