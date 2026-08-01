# MkDocs documentation site

Date: 2026-08-01

## Problem

k0smos has about 1560 lines of documentation across four files that grew
independently: `README.md` (426), `docs/usage.md` (493), `docs/architecture.md`
(368) and `docs/deployment.md` (273). Three problems follow from that.

The same material is in two places. The data volume is documented in the README
and again in usage. KubeVirt and bare metal appear in usage and again in
deployment. Two copies drift, and the KubeVirt sections already disagree about
whether a `containerDisk` is needed.

Reference material is buried in prose. The kernel cmdline table and the script
environment variables are in the README, between a tutorial and a debugging
guide, which is neither where a first-time reader wants them nor where someone
looking up a flag will look.

There is no rendered site. Everything is read on GitHub, so there is no search,
no navigation, and no way to publish documentation for a released version that
differs from `main`.

## Goals

Publish a documentation site built with MkDocs, following the setup k0s and
k0smotron use, from the documentation that already exists. Restructuring and
de-duplicating existing prose is in scope; writing substantial new material is
not, beyond the three pages named below.

Not in scope: a FAQ, a contributor guide beyond what the README already has, and
any documentation for features that do not exist.

## Decisions

Taken with the user during design:

| Decision | Choice |
|---|---|
| Content | Restructure what exists |
| Publishing | mike-versioned GitHub Pages |
| README | Short landing page; the site has its own Overview |
| Version macros | Yes |
| Mermaid | Yes |
| Dev container | Yes |
| Hash-pinned requirements | Yes |
| Nav shape | Mirror k0s |

mike now rather than later: switching a flat `gh-pages` to mike's per-version
layout afterwards means migrating published URLs, and k0smos has exactly one
release (`v0.0.1`), so the cost of doing it now is the lowest it will ever be.

## Machinery

`mkdocs.yml` lives at the repository root with `docs_dir: docs/`, because MkDocs
refuses a config file inside its own docs directory. Files added:

| Path | Purpose |
|---|---|
| `mkdocs.yml` | site config: material theme, nav, macros, mermaid, mike |
| `docs/requirements.txt` | pinned MkDocs stack |
| `docs/requirements_pip.txt` | hash-pinned pip and wheel |
| `docs/Makefile` | `docs` target: `cd .. && mkdocs build --strict` |
| `docs/Makefile.variables` | python and alpine versions for the dev container |
| `docs/Dockerfile.serve-dev` | containerised `mkdocs serve` |
| `docs/macros.py` | supplies `k0smos_version` |
| `docs/stylesheets/extra.css` | theme overrides |
| `docs/custom_theme/` | template overrides |
| `.github/workflows/docs.yml` | build on PRs touching `docs/` or `mkdocs.yml` |
| `.github/workflows/publish-docs.yml` | mike deploy |

Theme configuration follows k0smotron: material, light and dark palette toggles,
`toc.autohide`, `search.suggest`, `search.highlight`, `content.code.copy`.
Markdown extensions follow both repos: `pymdownx.highlight`, `superfences` with a
mermaid custom fence, `inlinehilite`, `mdx_truly_sane_lists`, `toc` with
permalinks to depth 3, `admonition`, `footnotes`.

Three deliberate departures from a blind copy:

**`docs/superpowers/` is excluded.** It holds specs and implementation plans,
including this document. It is internal and must not be published. Without an
exclude glob MkDocs picks up every markdown file under `docs_dir`.

**Root `make docs` delegates to `docs/Makefile`.** The reference repos have no
root Makefile worth speaking of; k0smos's root Makefile is the entry point for
every other task, so `make docs` has to work from the root. The real target
stays in `docs/Makefile` so the layout still matches.

**No `k0s_version` macro.** `image/fetch-k0s.sh` resolves the latest stable k0s
release at build time and pins nothing, so a version macro would assert a pin
that does not exist. Documentation says "latest stable". `k0smos_version` is
real, comes from `git describe --tags --abbrev=0`, and is overridable through
the `K0SMOS_VERSION` environment variable so CI can set it explicitly.

## Publishing

`docs.yml` runs on pull requests touching `docs/**` or `mkdocs.yml`: install
pinned requirements, `make -C docs docs`. `--strict` makes a broken internal
link or a nav entry pointing at a missing file fail the build.

`publish-docs.yml` runs on push to `main` and on published releases:

- push to `main`: `mike deploy --push head`
- release published, not a draft or prerelease: `mike deploy --push <tag>`
- then `mike alias -u head main`, `mike alias -u <newest stable> stable`,
  `mike set-default --push stable`

`site_url` is `https://makhov.github.io/k0smos/` and `repo_url` is
`https://github.com/makhov/k0smos`.

Requires one manual step outside this work: GitHub Pages must be enabled for the
repository, serving from the `gh-pages` branch. The first `mike deploy` creates
the branch; Pages will not serve it until it is switched on in repository
settings.

## Content

Structure mirrors k0s. Sources are given so the move can be checked page by
page.

```
Overview                     new, short
Installation
  Quick start                README "Quick start"
  What gets built            README "What gets built"
  Which kernel               README "Which kernel"
Usage
  The CLI                    usage "The CLI" + README "Talking to a running node"
  Boot a node locally        usage "Boot a node locally"
  Configure with cloud-init  usage "Configure a node with cloud-init"
  Ship manifests             usage "Ship Kubernetes manifests"
  The data volume            usage "Give it a data volume" + README "The data volume"
  Reach the cluster          usage "Reach the cluster from the host"
  A multi-node cluster       usage "More than one node"
  Shut it down               usage "Shut it down"
Deployment
  What you ship              deployment "What you ship"
  KVM / libvirt              deployment "KVM / libvirt"
  KubeVirt and Cluster API   deployment "KubeVirt..." + usage "Run it on KubeVirt"
  Bare metal                 deployment "Bare metal" + usage "Run it on bare metal"
  Cloud                      deployment "Cloud"
  Per-machine configuration  deployment "Per-machine configuration"
  Operating it               deployment "Operating it"
Reference
  Kernel cmdline options     README "Kernel cmdline options"
  k0smosctl                  new, from --help output
  Script environment vars    README "Script environment variables"
  The read-only root         README "The read-only root (erofs)"
Design
  The boot chain             architecture "The boot chain"
  Design decisions           architecture, the "Why ..." sections
  Testability                architecture "Testability"
Troubleshooting              usage "When something goes wrong" + README "Debugging"
Known limitations            README "Limitations" + deployment "Still missing for production"
Contributing
  Repository layout          README "Layout"
  Running the tests          README "Tests"
  Make targets               README "Make targets"
```

Every heading in the four source documents appears above exactly once, so a
section dropped during the move is visible as a heading with no destination.

Two placements worth stating, because neither is obvious:

**The read-only root goes in Reference, not Design.** The README section is
mostly operational — which `ROOTFS` value produces what, and which kernels can
mount it — with the rationale already covered by architecture's "Why a read-only
root works at all". Reference is where someone choosing a build option looks.

**Design decisions is one page, not ten.** architecture.md's "Why ..." sections
total under 300 lines and are read in sequence; splitting them into a page each
would make every one too small to stand alone.

### New prose

Three pages are not a move:

**Overview.** What k0smos is, what it is for, and where to go next. Short. It
takes over the framing the README currently does at length.

**Troubleshooting.** Merges two sources that overlap: usage's "When something
goes wrong" is symptom-first, the README's "Debugging" is tool-first. Result is
symptom-first with the tools named where each applies.

**k0smosctl reference.** The commands are described in prose today and there is
no one place listing them with their flags. Flag tables are generated from
`--help` output rather than transcribed, so they cannot drift from the code.

### De-duplication

Where two sources cover the same ground, one page results and the other's URL
does not survive. The three merges are named above: the data volume, KubeVirt,
and bare metal. Where the two copies disagree, the code decides — the KubeVirt
sections disagree today about whether a `containerDisk` is required, and
`image/mkoci.sh` builds one image carrying kernel and initramfs, with `/disk`
present only when `IMG=` is given.

### README

Reduced to a landing page: what k0smos is, a quick start, and a link to the
site. Everything else moves. The site's Overview is a separate file rather than
the README, so neither has to read well in both places.

The quick start exists twice, and deliberately. The README's is the shortest
sequence that boots a node — enough for someone who found the repository and
wants to see it run — and ends with a link. The site's Installation page is the
full version with prerequisites, what each step produces, and what to do when a
step fails. Keeping five lines in sync is a smaller cost than a README that
tells a visitor to go elsewhere before showing them anything.

## Verification

`mkdocs build --strict` in CI on every PR touching documentation. It fails on
broken internal links, nav entries pointing at files that do not exist, and
files under `docs_dir` that no nav entry references — which is what catches a
page dropped during the move.

Beyond the build: every code block asserting behaviour must match the code it
describes. The KubeVirt disagreement is the known instance; the move is the
occasion to check the rest against what is in the repository, since prose that
was accurate when written is the main thing a restructure quietly preserves.

No automated link checking of external URLs. Both reference repos live without
it and it fails on rate limits more often than on real breakage.

## Risks

**Published URLs change.** Nobody links to these documents yet — the site does
not exist and the repository has one release — so the cost is close to zero, but
it is only zero if it happens now.

**The dev container needs to be exercised, not just built.** An image that
builds but cannot serve looks healthy in CI. Implementation verifies it by
serving the site from the container and fetching a page, and CI builds it on
every docs PR following k0smotron's `dev-container` job.

**Generated flag tables need regenerating.** A `k0smosctl` reference built from
`--help` goes stale when a flag changes. Accepted for now: the alternative is
transcribing them by hand, which goes stale the same way and more quietly.
