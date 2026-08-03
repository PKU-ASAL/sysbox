# README Narrative Redesign

## Goal

Rewrite the project entry point around the problem Sysbox solves, then show the
capabilities that follow from its architecture. The README must help security
researchers, systems experimenters, and infrastructure developers understand
the project before introducing implementation terminology.

The primary README remains Chinese. A complete English README uses the same
structure and facts, and both files link to each other in the first viewport.

## Narrative Principle

Use "problem-driven, capability-grounded" ordering:

1. Mixed container, microVM, VM, and network experiments are difficult to
   reproduce, change safely, observe, reset, and clean up with scripts alone.
2. Sysbox lets users declare the intended topology and manages its lifecycle as
   one stateful system.
3. Concrete workflows and supported environments demonstrate that claim.
4. Provider, capability driver, and Agent terminology appears only after the
   reader understands why those boundaries exist.

Avoid presenting Sysbox as only a scheduler. It also owns graph construction,
planning, state, observation, recovery, reset, and safe deletion.

## README Structure

The Chinese and English files share this order:

1. Project name, language switch, and a plain-language value statement.
2. The problem: why heterogeneous experiments become fragile when assembled
   from independent scripts and tools.
3. How Sysbox works in four steps: declare, plan, apply, observe/recover.
4. What users can build: mixed Docker, Firecracker, libvirt, Linux networking,
   routing, NAT, and policy topologies.
5. Quick start using the lowest-dependency Docker example.
6. A small HCL model illustrating network, image, node, and dependency intent.
7. Architecture and extension boundaries, introducing resource handlers,
   capability drivers/providers, and Agents.
8. Supported scope and explicit non-goals.
9. Documentation, verification, contribution guidance, and license.

The README stays an entry point. Detailed field definitions, deployment steps,
provider requirements, and recovery procedures remain in their canonical docs.

## Capability Claims And Boundaries

The README may claim topology-level heterogeneity because one graph can contain
Docker containers, Firecracker microVMs, libvirt VMs, and shared network/policy
resources. It must also state the current scheduling boundary:

- providers implement typed runtime capabilities;
- host Agents advertise the capabilities available on that execution host;
- the scheduler assigns an entire topology run to one Agent satisfying the
  topology's combined requirements;
- Sysbox does not currently place individual nodes from one topology across
  multiple Agents or provide a general multi-host overlay network.

"Real scenarios" means controlled Linux security, networking, and systems
experiments with observable lifecycle semantics. It does not mean arbitrary
cloud resources, arbitrary Terraform providers, every guest OS, or a general
production cloud orchestrator.

## Content And Style Constraints

- Lead with user problems and outcomes, not HCL, provider names, or internal
  architecture nouns.
- Prefer short paragraphs and concrete verbs over slogans.
- Keep the first runnable path copy-pasteable.
- Do not duplicate reference documentation.
- Keep Chinese and English claims semantically equivalent.
- Preserve existing canonical documentation links and add a clear contribution
  entry point.
- Do not claim architectural guarantees that the implementation does not yet
  provide.

## Verification

- Check all relative links in both README files.
- Run the repository documentation test if available.
- Confirm commands and referenced example paths exist.
- Compare section order and capability/boundary claims across both languages.
- Scan for stale claims such as node-level cross-Agent scheduling or arbitrary
  provider compatibility.
