// Smoke test: exercise the idempotent MCP registration logic.
// Runs `yacttExtension(pi)` with a fake `pi` and asserts mcp.json is
// written correctly the first time and left alone the second time.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import yacttExtension from "../index.js";

function fakePi() {
	const handlers = {};
	return { on: (e, fn) => { handlers[e] = fn; }, _fire: (e) => handlers[e]?.() };
}

function withTmpAgentDir(fn) {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "yactt-pi-test-"));
	const oldAgent = process.env.PI_CODING_AGENT_DIR;
	const oldBoot = process.env.YACTT_BOOTSTRAP;
	process.env.PI_CODING_AGENT_DIR = dir;
	// ponytail: skip the network bootstrap in tests; we only exercise the MCP wiring.
	process.env.YACTT_BOOTSTRAP = "/nonexistent";
	const restore = () => {
		process.env.PI_CODING_AGENT_DIR = oldAgent;
		process.env.YACTT_BOOTSTRAP = oldBoot;
		fs.rmSync(dir, { recursive: true, force: true });
	};
	try { return fn(dir); } finally { restore(); }
}

test("session_start writes mcp.json with the yactt server", () => {
	withTmpAgentDir((dir) => {
		fs.writeFileSync(path.join(dir, "settings.json"), JSON.stringify({ packages: ["npm:pi-mcp-adapter"] }));
		const pi = fakePi();
		yacttExtension(pi);
		pi._fire("session_start");
		const cfg = JSON.parse(fs.readFileSync(path.join(dir, "mcp.json"), "utf8"));
		assert.deepEqual(cfg.mcpServers.yactt, {
			command: "yactt",
			args: ["mcp", "serve"],
			lifecycle: "keep-alive",
		});
	});
});

test("session_start is idempotent and preserves user-added servers", () => {
	withTmpAgentDir((dir) => {
		fs.writeFileSync(path.join(dir, "settings.json"), JSON.stringify({ packages: ["npm:pi-mcp-adapter"] }));
		const cfgPath = path.join(dir, "mcp.json");
		fs.writeFileSync(cfgPath, JSON.stringify({
			mcpServers: { other: { command: "other", args: [] } },
		}));
		const pi = fakePi();
		yacttExtension(pi);
		pi._fire("session_start");
		pi._fire("session_start"); // second time: must not duplicate or clobber
		const cfg = JSON.parse(fs.readFileSync(cfgPath, "utf8"));
		assert.ok(cfg.mcpServers.other, "user-added server preserved");
		assert.ok(cfg.mcpServers.yactt, "yactt server registered");
		assert.equal(Object.keys(cfg.mcpServers.yactt).length, 3);
	});
});

test("session_start tolerates a corrupt mcp.json", () => {
	withTmpAgentDir((dir) => {
		fs.writeFileSync(path.join(dir, "settings.json"), JSON.stringify({ packages: ["npm:pi-mcp-adapter"] }));
		fs.writeFileSync(path.join(dir, "mcp.json"), "{not json");
		const pi = fakePi();
		yacttExtension(pi);
		pi._fire("session_start");
		const cfg = JSON.parse(fs.readFileSync(path.join(dir, "mcp.json"), "utf8"));
		assert.ok(cfg.mcpServers.yactt);
	});
});

test("ensureMcpAdapter is a no-op when settings already lists pi-mcp-adapter", () => {
	withTmpAgentDir((dir) => {
		fs.writeFileSync(path.join(dir, "settings.json"), JSON.stringify({ packages: ["npm:pi-mcp-adapter"] }));
		const pi = fakePi();
		yacttExtension(pi);
		// Should NOT print "yactt: installing pi-mcp-adapter..." — that means it
		// would shell out to `pi install`, which we don't want in tests.
		const original = console.log;
		let called = false;
		console.log = () => { called = true; };
		try { pi._fire("session_start"); } finally { console.log = original; }
		assert.equal(called, false, "should not have invoked `pi install`");
	});
});