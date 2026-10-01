workspace "CyberPenda" "Current-state container and component model of the CyberPenda local-first pentest agent and the TSecBench Hosted Image. Evidence index: system-model.evidence.md." {
    !identifiers hierarchical

    model {
        operator = person "Operator" "Security tester running authorized engagements from their own machine."

        modelProvider = softwareSystem "Model Provider" "External model service or gateway (OpenAI-compatible or Anthropic protocol)." "External"
        containerEngine = softwareSystem "Container Engine" "Docker, Podman, or OrbStack. Runs Sandbox task containers." "External"
        challengePlatform = softwareSystem "Challenge Platform (TSecBench)" "Issues Benchmark Challenges, accepts submissions, keeps score." "External"
        targetSystems = softwareSystem "Authorized Target Systems" "Assets inside the Project Scope that Runtimes are authorized to test." "External"

        cyberpenda = softwareSystem "CyberPenda" "Local-first pentest agent control plane." {
            spa = container "React Dashboard" "Project dashboard, Run Controls, Blackboard, Findings, Report, Skills, Model Providers. Served embedded from pentestd in release builds; Vite dev server in development." "React 19 + Vite + Tailwind"
            pentestd = container "pentestd Daemon" "Local HTTP control plane: REST API, auth, Runtime Harness, FGS receiver, domain services, embedded UI." "Go 1.25" {
                httpApi = component "HTTP API" "v1 routes, Blackboard v2 routes, FGS read routes; bearer token or loopback operator session. /mcp is retired and returns 404." "internal/daemon"
                harness = component "Runtime Harness" "Launches, resumes, steers, and stops one Runtime per Runtime Owner; owns process lifecycle; executes no pentest tools." "internal/runtime"
                runnerProjection = component "Runner + Config Projection" "Owner-local directories, prepared Config Projection for native files and process environment, read-only Preflight, Skill and Extension projection, separate launch-command construction." "internal/runner, internal/preflight"
                fgs = component "FGS Store" "Accepted Goal/Step/Fact updates with delivery Receipts (Runtime Outbox settlement)." "internal/fgs"
                blackboard = component "Blackboard v2 Service" "Durable semantic memory: Entities, Project Facts, Findings, Solutions, Relationships." "internal/blackboardv2 (+contract/grammar/input)"
                domains = component "Domain Services" "Project, Scope, Task, Session, Skill, Model Provider, Credential, Report, Steering, Finish Readiness." "internal/{project,task,session,skill,modelprovider,credential,report,steering,finishreadiness}"
                store = component "Store" "SQLite connection and schema migrations shared by every domain package; persistence stays inline in domain services (ADR 0017)." "internal/store"
            }
            db = container "SQLite Database" "pentest.db: projects, tasks, sessions, continuations, runtime config versions, runtime profiles, skills, credentials, blackboard_v2_*, fgs_* tables." "SQLite (modernc.org/sqlite)" {
                tags "Database"
            }
            runtime = container "Agent Runtime" "Codex, Claude Code, or Pi CLI; one persistent Runtime per Runtime Owner. Runs inside the Sandbox Runner container (default, Kali image) or the Host Runner (explicit opt-in)." "Provider CLI"
            bridges = container "Provider Bridges" "pentest-provider-bridge (Go JSON-RPC/stdio shim for Codex and Pi) and pentest-claude-sdk-bridge (Node.js bridge over the Claude Agent SDK)." "Go / Node.js"
            pentestctl = container "pentestctl CLI" "FGS publication and reads inside a projected Runtime: working-graph emit/read/status/history. Retained legacy blackboard commands are not the FGS write path." "Go"
        }

        hostedImage = softwareSystem "TSecBench Hosted Image" "Separate, self-starting linux/amd64 distribution for one Hosted Evaluation Run." {
            hostedController = container "Hosted Controller" "Bootstrap and observation for one evaluation Project and Task; validates Hosted Model Configuration, emits the Hosted Transcript Stream (JSONL stdout)." "pentest-tsecbench-hosted (Go)"
            hostedDaemon = container "In-process pentestd" "The same daemon server run in-process by the Hosted Controller; seeds the evaluation Project and Task." "internal/daemon"
            challengeClient = container "Hosted Challenge Client" "Process-isolated, bounded platform operations: list/start/hint/submit/close/abandon." "pentest-tsecbench-client (Go)"
            hostedRuntime = container "Hosted Runtime" "Bundled Codex / Claude Code / Pi with the Hosted Tool Baseline (bounded Kali toolset) and read-only offline reference data under /opt/knowledge." "Provider CLI + Kali toolset"
        }

        operator -> cyberpenda.spa "Uses the dashboard" "HTTPS"
        cyberpenda.spa -> cyberpenda.pentestd "REST API calls" "JSON/HTTP /api"
        cyberpenda.pentestd -> cyberpenda.db "Reads/writes" "SQL"
        cyberpenda.pentestd -> containerEngine "Creates and controls Sandbox task containers" "Docker/Podman CLI"
        cyberpenda.pentestd -> cyberpenda.runtime "Launches, resumes, steers, stops" "process stdio (Runtime Harness)"
        cyberpenda.pentestd -> cyberpenda.bridges "Owns bridge processes" "stdio"
        cyberpenda.bridges -> cyberpenda.runtime "Frames provider protocol sessions" "JSON-RPC / JSONL"
        cyberpenda.runtime -> cyberpenda.pentestctl "Publishes FGS updates and reads Blackboard state" "CLI invocation"
        cyberpenda.pentestctl -> cyberpenda.pentestd "Continuation Interface: emit/read/status/history" "HTTP"
        cyberpenda.runtime -> modelProvider "Model calls with projected credentials" "HTTPS"
        cyberpenda.runtime -> targetSystems "Authorized testing within Project Scope" "tool-specific"

        challengePlatform -> hostedImage.hostedController "Injects BENCHMARK_BASE_URL and one-use BENCHMARK_TOKEN; consumes the JSONL transcript" "stdout/HTTPS"
        hostedImage.hostedController -> hostedImage.hostedDaemon "Boots in-process daemon; seeds the evaluation Project and Task" "in-process"
        hostedImage.hostedDaemon -> hostedImage.hostedRuntime "Launches the Hosted Runtime" "process"
        hostedImage.hostedController -> hostedImage.challengeClient "Runs one bounded platform operation per process" "process exec"
        hostedImage.challengeClient -> challengePlatform "list/start/hint/submit/close/abandon" "HTTPS"
        hostedImage.hostedRuntime -> modelProvider "Model calls through the evaluation gateway" "HTTPS"

        cyberpenda.pentestd.httpApi -> cyberpenda.pentestd.domains "Domain handlers" "in-process"
        cyberpenda.pentestd.httpApi -> cyberpenda.pentestd.blackboard "Blackboard v2 routes" "in-process"
        cyberpenda.pentestd.httpApi -> cyberpenda.pentestd.fgs "FGS read routes" "in-process"
        cyberpenda.pentestd.httpApi -> cyberpenda.pentestd.harness "Task launch, steering, runtime lifecycle" "in-process"
        cyberpenda.pentestd.harness -> cyberpenda.pentestd.runnerProjection "Launch preparation (Preflight, Config Projection)" "in-process"
        cyberpenda.pentestd.domains -> cyberpenda.pentestd.store "Inline persistence" "in-process"
        cyberpenda.pentestd.blackboard -> cyberpenda.pentestd.store "Semantic records" "in-process"
        cyberpenda.pentestd.fgs -> cyberpenda.pentestd.store "Accepted updates and Receipts" "in-process"
        cyberpenda.pentestd.store -> cyberpenda.db "SQL" "modernc.org/sqlite"
        cyberpenda.pentestctl -> cyberpenda.pentestd.httpApi "working-graph emit/read/status/history" "HTTP"
    }

    views {
        container cyberpenda "CyberPendaContainers" "Deployable/runnable units of CyberPenda." {
            include *
            autoLayout lr
        }

        component cyberpenda.pentestd "PentestdComponents" "Major internal responsibilities of the pentestd daemon (current state; retired packages listed in the evidence file)." {
            include *
            autoLayout tb
        }

        container hostedImage "HostedImageContainers" "Inside the TSecBench Hosted Image." {
            include *
            autoLayout lr
        }

        styles {
            element "Person" {
                shape person
                background #08427b
                color #ffffff
            }
            element "External" {
                background #999999
                color #ffffff
            }
            element "Database" {
                shape cylinder
                background #2e5e4e
                color #ffffff
            }
            element "Component" {
                background #85bbf0
                color #000000
            }
        }
    }
}
