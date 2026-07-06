# Crystal Ball — yactt Claude Code plugin

> Stage 2 of the GRFP workflow for `plugins/yactt/README.md`.
> Generated 2026-07-05.

**Graph tools available:** Yes (same index as root project).
**Method used**: graph (primary); all six plugin files read in Stage 1.
**Counterpart report**: `.claude/grfp/plugin-deep-dive.md`.

---

## 1. Where this plugin sits in the ecosystem

Most Claude Code plugins fall into three buckets:

1. **Skill-only** — a `SKILL.md` that teaches the agent a workflow.
2. **MCP-server-only** — an `mcp.json` that wires an external server.
3. **Skill + MCP combined** — a curated experience around a server.

yactt is bucket 3, but with one addition: **a self-bootstrapping install story** (the SessionStart hook that downloads the binary). Most MCP-only plugins assume the user already has the server binary on disk. yactt's plugin installs it.

This positions the plugin in a small niche: **MCP servers that ship a complete install experience via the plugin marketplace**, not just a config. The closest cousins are tools that bundle their own LSP server, formatter, or linter.

**Source**: `plugins/yactt/hooks/hooks.json` (bootstrap hook); `plugins/yactt/.mcp.json` (server wiring); compared with typical plugin shapes via observed patterns.

---

## 2. Audience segments (beyond the README's primary)

| Audience | Today | Could | Why |
|---|---|---|---|
| Claude Code users | ✓ primary | — | The install command targets them |
| Plugin developers (working on yactt itself) | ✓ escape hatch exists | README should surface it | `<repo>/bin/yactt` shortcut isn't documented |
| Security-conscious developers | ✓ integrity model is centerpiece | "gh attestation verify" in bootstrap (tracked) | SLSA L3 is the right answer for them |
| Offline / air-gapped users | ✗ bootstrap fails without network | document manual install | Install script can't reach GitHub API |
| Windows users | ✗ script only handles darwin/linux | add Windows support | `uname -s` returns `MINGW*`/`CYGWIN*`/etc., falls through |
| Additional architectures (armv7, riscv64, etc.) | ✗ only arm64 + amd64 | extend matrix | `case "$(uname -m)"` is the extension point |
| Plugin marketplace curators | ✓ works with `marketplace add` | — | Standard pattern |

**Source**: `plugins/yactt/scripts/install.sh:85–100` (`os_arch` function — the only platform detection).

---

## 3. Dead-code analysis

The plugin is too small and too declarative for the graph's `max_degree: 0` filter to surface meaningful orphans. All six files are either:

- **Manifests** (`.json`) — entry points by design.
- **Documentation** (`README.md`, `SKILL.md`) — entry points by design.
- **The bootstrap script** — entry point by design.

There is no production function-level code. **No dead-code findings.**

The closest thing to "low-traffic code" is the `cache_age` helper inside `install.sh` — called twice during the same run, never across runs. That's normal caching code, not dead code.

**Source**: complete file inventory in `plugin-deep-dive.md` § 4.

---

## 4. Complexity hotspots

### Single-file complexity: `scripts/install.sh`

| File | Lines | Notable |
|---|---|---|
| `plugins/yactt/scripts/install.sh` | **192** | The only file with logic. 6 sub-functions, `set -euo pipefail`, careful `trap` teardown, well-commented. |

The script's complexity isn't algorithmic — it's the *threat surface*. 192 lines of "how do we download a binary safely" is reasonable for the problem domain.

### Sub-function complexity (within `install.sh`)

| Function | Lines | Concern |
|---|---|---|
| `download_and_install` | ~50 | SHA256SUMS-first, then binary, then verify, then install, then TOFU record. 5 distinct steps. Clear ordering. |
| `latest_version` | ~20 | Cache check, semver allowlist, API call, cache write. The semver allowlist is the security highlight. |
| `os_arch` | ~15 | Hard-coded matrix. Extension point for new platforms. |
| `is_semver` | ~5 | Regex allowlist. Defends against poisoned cache. |
| `cache_age` | ~5 | Trivial. |
| `installed_version` | ~5 | Trivial. |

**The script has good function-level decomposition** — each function does one thing, each is short, each has a comment block explaining intent.

### Callers / popularity

| Symbol | Fan-in | Note |
|---|---|---|
| `main` (in `install.sh`) | 1 | The single entry point |
| `download_and_install` | 1 | Called by `main` |
| `latest_version` | 1 | Called by `main` |
| `os_arch` | 1 | Called by `main` |
| `cache_age` | 2 | `latest_version` calls it twice (once for fresh-cache check, once for TTL re-check after read) |

**Source**: read of full `install.sh` in Stage 1.

---

## 5. Attack surface analysis

This is the meat of the plugin's crystal-ball view. The SessionStart hook runs `install.sh` automatically on every Claude Code session. The script does the following network and disk operations:

```
                   ┌─────────────────────────────────┐
                   │  SessionStart hook (auto-run)    │
                   └─────────────────┬───────────────┘
                                     ▼
                   ┌─────────────────────────────────┐
                   │ 1. stat $CLAUDE_PROJECT_DIR/bin  │  ← developer escape hatch
                   └─────────────────┬───────────────┘
                                     ▼ (if not present)
                   ┌─────────────────────────────────┐
                   │ 2. read $CACHE_FILE (1h TTL)    │  ← unauthed API cache
                   └─────────────────┬───────────────┘
                                     ▼ (on cache miss)
                   ┌─────────────────────────────────┐
                   │ 3. GET api.github.com/repos/...  │  ← public, unauthenticated
                   └─────────────────┬───────────────┘
                                     ▼
                   ┌─────────────────────────────────┐
                   │ 4. GET SHA256SUMS                │  ← same origin as binary
                   └─────────────────┬───────────────┘
                                     ▼
                   ┌─────────────────────────────────┐
                   │ 5. TOFU check vs known-good      │  ← same-version replay defense
                   └─────────────────┬───────────────┘
                                     ▼ (TOFU passes)
                   ┌─────────────────────────────────┐
                   │ 6. GET <archive>.tar.gz          │  ← binary download
                   └─────────────────┬───────────────┘
                                     ▼
                   ┌─────────────────────────────────┐
                   │ 7. shasum -a 256 verify          │  ← corruption detection
                   └─────────────────┬───────────────┘
                                     ▼
                   ┌─────────────────────────────────┐
                   │ 8. install -m 0755 → $INSTALL    │  ← writes to disk
                   └─────────────────┬───────────────┘
                                     ▼
                   ┌─────────────────────────────────┐
                   │ 9. record (version, sha) → known-good │
                   └─────────────────────────────────┘
```

### Threat model

| Threat | Defense | Confidence |
|---|---|---|
| Transport corruption (partial download, bit flips) | SHA256 mismatch in `SHA256SUMS` | **High** — covered |
| Same-version replay (binary swap, hash swap) | TOFU check on `known-good` | **High** — covered |
| DNS poisoning / TLS MITM | System root CAs (assumes user has a sane trust store) | **High** — inherited from the OS |
| Malicious **new** release (attacker compromises a maintainer account, ships a "v0.2.0" with a backdoor) | **None automated.** Manual `gh attestation verify` is the path. SLSA L3 attestations close this gap, but the bootstrap doesn't call `gh` yet. | **Low** — gap |
| Poisoned GitHub cache (someone plants a fake version in `$CACHE_FILE`) | `is_semver` allowlist refuses anything non-semver; cache write only happens after API success | **Medium** — covered for non-semver, but a *valid* semver from a compromised API would still flow through |
| Local privilege escalation via `install -m 0755` | The script uses `$XDG_HOME` / `$HOME/.local/bin` which the user already owns; no sudo | **High** — covered |
| Re-running the script for downgrade | Bootstrap always upgrades to `latest`; rollback is **not supported** today | **Low** — gap |
| TOFU file tampering | The `known-good` file lives in `$XDG_DATA_HOME`, owned by the user. Tampering requires local root. | **High** — covered |

### Critical gap: bootstrap doesn't verify SLSA attestations

This is the single biggest follow-up. The README documents this as "follow-up work" but the bootstrap script doesn't yet invoke `gh attestation verify`. Until it does, the threat of a malicious new release is **not covered by automation** — only by manual user verification.

**Source**: `plugins/yactt/scripts/install.sh:14–22` (TOFU comment); `plugins/yactt/README.md:21–42` (integrity model section, including the "what it doesn't catch" list).

---

## 6. Roadmap candidates

Items the plugin is *already pointed at* — no speculation, just carried-over TODOs and unaddressed threat-model gaps.

| Candidate | Location | Maturity |
|---|---|---|
| **`gh attestation verify` in the bootstrap** | `install.sh` comments (line 14–22, 96–98) | Tracked; explicit follow-up |
| **Windows support** | `install.sh:os_arch` — only `Darwin` and `Linux` cases; rest returns 1 | Tracked implicitly via "unsupported OS" message |
| **Additional architectures** (armv7, riscv64, ppc64le) | `install.sh:os_arch` — only `arm64|aarch64` and `x86_64|amd64` | Tracked implicitly |
| **Upgrade rollback** | `install.sh` always upgrades to `latest` | Not supported today |
| **Skill versioning** | `code-explore/SKILL.md` frontmatter — no version field | Tracked implicitly (SKILL frontmatter schema) |
| **Out-of-band manifest signing** (cosign/minisign on `SHA256SUMS`) | `install.sh:14–22` comment explicitly calls this out | Tracked |
| **Offline / air-gapped install story** | None today | User would need to vendor the binary manually |
| **Version pinning** (`/plugin install yactt@yactt@v0.2.0`) | Plugin marketplace supports it; install.sh always grabs latest | Tracked implicitly |
| **Self-test / smoke test** after install | install.sh installs and exits; no `yactt overview <repo>` smoke test | Optional quality-of-life |
| **Multi-plugin marketplace packaging** | The plugin could be one of several | Future |

**Sources**: `plugins/yactt/scripts/install.sh:14–22, 96–98, 85–100`; `plugins/yactt/README.md:21–42` (integrity model).

---

## 7. What's already shipped (worth a README mention)

- v0.1.0 (`plugin.json`)
- SessionStart bootstrap (`hooks/hooks.json`)
- TOFU on same-version replay (`scripts/install.sh`)
- SHA256SUMS corruption detection (`scripts/install.sh`)
- Developer escape hatch (`<repo>/bin/yactt` short-circuit)
- Cross-platform darwin/linux × arm64/amd64
- 1-hour cache for unauthed GitHub API
- Semver allowlist defense against poisoned cache
- `code-explore` skill frontmatter that triggers on the right prompts
- SLSA L3 attestations on every release (manual verification only)

**Source**: full file inventory in `plugin-deep-dive.md`.

---

## 8. README angles worth elevating

Given the deep-dive + this crystal-ball view, the README's standout angles should be:

1. **The headline**: "yactt for Claude Code" — the plugin is one of the project's two READMs; this isn't a separate project, it's the user-facing install story.
2. **The threat matrix**: keep it. It's the README's signature, and no competitor writes one.
3. **The developer escape hatch**: surface it. Plugin authors will hit it.
4. **The skill story**: the `code-explore` skill is the canonical teaching surface for the agent — it deserves a section.
5. **The update / uninstall story**: a 3-line note. The bootstrap upgrades on version mismatch; uninstall is two `rm`s.
6. **Pointer to the root README**: for the empty-quadrant positioning, the architecture, and the trust-signals section.
7. **The roadmap snippet**: what's next on the bootstrap side (`gh attestation verify` integration, Windows support, etc.).

### What we explicitly chose NOT to do

- Not opening with "Federated code-intelligence MCP server + code-explore skill for Claude Code." Too clinical for a Claude Code plugin README; the audience already knows what MCP is.
- Not duplicating the 10-tool table from the root README. Keep it (the table is short and useful), but link to the root README for the framing.
- Not advocating for security theater. The threat matrix is honest about what it catches and what it doesn't.
- Not opening with the `gh attestation verify` one-liner. It belongs in the integrity model section, not the install section (it's not yet automated).

---

## 9. Trust / risk flags worth a README mention

- **Network requirement**: bootstrap needs unauthed GitHub API access + tarball download. No network → plugin doesn't work.
- **Disk footprint**: ~30-100 MB per binary install; ~1 KB for the cache + TOFU file.
- **One install path per user**: `$XDG_HOME/bin/yactt` (or `$HOME/.local/bin/yactt`). No multi-install.
- **Bootstrapping is idempotent**: re-running is safe and cheap (cache hits in 1h, otherwise ~2 MB SHA256SUMS + binary tarball).
- **Script never escalates**: never `sudo`, never writes outside `$XDG_HOME` / `$HOME`.
- **TOFU file persists across uninstalls**: removing the binary doesn't remove `${XDG_DATA_HOME}/yactt/known-good` — caller must do it explicitly.

**Source**: `scripts/install.sh` (all 192 lines).

---

## Ready for Stage 3

Brain Jam next — voice, tone, marketing angle for the *plugin* specifically. The plugin's reader is narrower than the root README's reader (Claude Code users only, not "AI agent authors" generally), and the threat-matrix centerpiece constrains how playful the tone can be.

Proceeding to **Stage 3: Brain Jam** on the plugin.