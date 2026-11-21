workspace "CyberPenda System Context" "Current-state system context of the CyberPenda local-first pentest agent. Evidence index: system-model.evidence.md." {

    model {
        operator = person "Operator" "Security tester running authorized engagements from their own machine."

        cyberpenda = softwareSystem "CyberPenda" "Local-first pentest agent: Go control plane (pentestd), React dashboard, and Codex / Claude Code / Pi Runtimes with Project Scope, approvals, and a Goal/Step/Fact Blackboard."

        modelProvider = softwareSystem "Model Provider" "External model service or gateway (OpenAI-compatible or Anthropic protocol). Runtimes call it directly with projected credentials; the daemon never proxies model traffic." "External"
        containerEngine = softwareSystem "Container Engine" "Docker, Podman, or OrbStack. Runs Sandbox task containers for the Sandbox Runner." "External"
        challengePlatform = softwareSystem "Challenge Platform (TSecBench)" "Issues Benchmark Challenges, accepts submissions, keeps score. Reached only by the TSecBench Hosted Image through the Hosted Challenge Client." "External"
        targetSystems = softwareSystem "Authorized Target Systems" "Assets inside the Project Scope that Runtimes are authorized to test." "External"
        ghcr = softwareSystem "GHCR Image Registry" "Publishes the app and sandbox images (ghcr.io/n1majne3/cyberpenda*). Distribution surface only." "Distribution"
        vercelDemo = softwareSystem "Vercel Demo Deployment" "Static, read-only demo build of the dashboard (vite build:demo mode). No daemon, no Runtimes, no tools." "Distribution"

        operator -> cyberpenda "Operates Projects, Tasks, Scope, and Settings in the browser" "HTTP loopback :8787"
        cyberpenda -> modelProvider "Runtime model calls with projected credentials" "HTTPS"
        cyberpenda -> containerEngine "Creates and controls Sandbox task containers" "Docker/Podman CLI"
        cyberpenda -> targetSystems "Runtime tool traffic from Sandbox or Host Runner, bounded by Scope" "tool-specific"
        cyberpenda -> challengePlatform "Hosted Challenge Client operations (Hosted evaluation only)" "HTTPS"
        cyberpenda -> ghcr "Pulls app and sandbox images (inferred: standard registry flow)" "HTTPS"
        cyberpenda -> vercelDemo "Dashboard source is deployed as a static demo build" "npm run build:demo"
    }

    views {
        systemContext cyberpenda "SystemContext" "CyberPenda boundary: operator, external systems, and distribution surfaces." {
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
            element "Distribution" {
                background #d3d3d3
                color #000000
            }
        }
    }
}
