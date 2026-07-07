// Bootstraps `yactt` on first pi session and registers it with
// pi-mcp-adapter. Idempotent thereafter — running `pi` a second time
// is a no-op.
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
function bootstrapScript() {
	// ponytail: lazy so tests can redirect via YACTT_BOOTSTRAP=/nonexistent.
	return process.env.YACTT_BOOTSTRAP || path.resolve(here, "../plugins/yactt/scripts/install.sh");
}
function agentDir() {
	// ponytail: resolved per-call so tests can redirect via env var.
	return process.env.PI_CODING_AGENT_DIR || `${process.env.HOME}/.pi/agent`;
}

function readJson(p) {
	try {
		return JSON.parse(fs.readFileSync(p, "utf8"));
	} catch {
		return {};
	}
}

function ensureMcpAdapter() {
	const settings = readJson(path.join(agentDir(), "settings.json"));
	const has = (settings.packages || []).some((p) => p.startsWith("npm:pi-mcp-adapter"));
	if (has) return;
	console.log("yactt: installing pi-mcp-adapter (MCP support)...");
	// ponytail: shell out to pi; rerunning is a safe no-op when already added.
	spawnSync("pi", ["install", "npm:pi-mcp-adapter"], { stdio: "inherit" });
}

function registerMcpServer() {
	const cfgPath = path.join(agentDir(), "mcp.json");
	const cfg = readJson(cfgPath);
	cfg.mcpServers ??= {};
	if (cfg.mcpServers.yactt) return; // ponytail: idempotent.
	cfg.mcpServers.yactt = {
		command: "yactt",
		args: ["mcp", "serve"],
		lifecycle: "keep-alive",
	};
	fs.mkdirSync(path.dirname(cfgPath), { recursive: true });
	fs.writeFileSync(cfgPath, JSON.stringify(cfg, null, 2) + "\n");
}

export default function yacttExtension(pi) {
	pi.on("session_start", () => {
		const script = bootstrapScript();
		if (fs.existsSync(script)) spawnSync("bash", [script], { stdio: "inherit" });
		ensureMcpAdapter();
		registerMcpServer();
	});
}
