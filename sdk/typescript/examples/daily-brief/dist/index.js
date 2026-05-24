// @bun
// ../../src/content.ts
function text(s) {
  return { type: "text", text: s };
}
function json(v) {
  return { type: "text", text: JSON.stringify(v) };
}
function toToolResult(value) {
  if (isToolResult(value)) {
    return value;
  }
  if (typeof value === "string") {
    return { content: [text(value)] };
  }
  return { content: [json(value)] };
}
function isToolResult(v) {
  if (v === null || typeof v !== "object")
    return false;
  const obj = v;
  return Array.isArray(obj.content);
}

// ../../src/jsonrpc.ts
var JSONRPC_VERSION = "2.0";
var ErrorCodes = {
  ParseError: -32700,
  InvalidRequest: -32600,
  MethodNotFound: -32601,
  InvalidParams: -32602,
  InternalError: -32603,
  PermissionDenied: -32001,
  ApprovalRequired: -32002,
  UnknownTool: -32003,
  BackendError: -32099
};

class RPCError extends Error {
  code;
  data;
  constructor(code, message, data) {
    super(message);
    this.name = "RPCError";
    this.code = code;
    this.data = data;
  }
}
function isRequest(msg) {
  return "id" in msg && "method" in msg;
}
function isNotification(msg) {
  return !("id" in msg) && "method" in msg;
}
function isResponse(msg) {
  return "id" in msg && (("result" in msg) || ("error" in msg));
}

// ../../src/runtime.ts
function createRuntimeClient(transport) {
  return {
    tools: {
      async list() {
        const res = await transport.request("tools/list");
        return res.tools ?? [];
      },
      async call(name, args) {
        if (!name) {
          throw new RPCError(-32602, "tools.call: name is required");
        }
        return await transport.request("tools/call", {
          name,
          arguments: args ?? {}
        });
      }
    },
    resources: {
      async list() {
        const res = await transport.request("resources/list");
        return res.resources ?? [];
      },
      async read(uri) {
        if (!uri) {
          throw new RPCError(-32602, "resources.read: uri is required");
        }
        return await transport.request("resources/read", {
          uri
        });
      }
    },
    log(level, msg, extra) {
      transport.notify("logging/message", {
        level,
        message: msg,
        ...extra ? { data: extra } : {}
      }).catch(() => {});
    }
  };
}

// ../../src/transport.ts
var noopLogger = {
  warn: () => {},
  error: () => {}
};

class Transport {
  handler;
  logger;
  input;
  writer;
  pending = new Map;
  encoder = new TextEncoder;
  nextID = 1;
  closed = false;
  closedDeferred;
  currentReader = null;
  constructor(opts) {
    this.handler = opts.handler;
    this.logger = opts.logger ?? noopLogger;
    this.input = opts.streams.input;
    this.writer = opts.streams.output.getWriter();
    this.closedDeferred = Promise.withResolvers();
  }
  async start() {
    const decoder = new TextDecoder;
    const reader = this.input.getReader();
    this.currentReader = reader;
    let buffer = "";
    try {
      while (!this.closed) {
        const { done, value } = await reader.read();
        if (done)
          break;
        buffer += decoder.decode(value, { stream: true });
        let nl;
        while ((nl = buffer.indexOf(`
`)) !== -1) {
          const line = buffer.slice(0, nl).trim();
          buffer = buffer.slice(nl + 1);
          if (line.length === 0)
            continue;
          this.dispatchLine(line);
        }
      }
    } catch (err) {
      if (!this.closed) {
        this.logger.error("transport read loop crashed", {
          error: errorToObj(err)
        });
      }
    } finally {
      this.currentReader = null;
      try {
        reader.releaseLock();
      } catch {}
      this.failPending(new Error("transport closed"));
      this.closedDeferred.resolve();
    }
  }
  async request(method, params) {
    if (this.closed) {
      throw new Error("transport: closed");
    }
    const id = this.nextID++;
    const frame = {
      jsonrpc: JSONRPC_VERSION,
      id,
      method,
      ...params !== undefined ? { params } : {}
    };
    const deferred = Promise.withResolvers();
    this.pending.set(id, deferred);
    try {
      await this.writeFrame(frame);
    } catch (err) {
      this.pending.delete(id);
      throw err;
    }
    return deferred.promise;
  }
  async notify(method, params) {
    if (this.closed) {
      throw new Error("transport: closed");
    }
    const frame = {
      jsonrpc: JSONRPC_VERSION,
      method,
      ...params !== undefined ? { params } : {}
    };
    await this.writeFrame(frame);
  }
  async close() {
    if (this.closed)
      return;
    this.closed = true;
    if (this.currentReader) {
      try {
        await this.currentReader.cancel();
      } catch {}
    }
    try {
      await this.writer.close();
    } catch {}
    await this.closedDeferred.promise;
  }
  get done() {
    return this.closedDeferred.promise;
  }
  dispatchLine(line) {
    let msg;
    try {
      msg = JSON.parse(line);
    } catch (err) {
      this.logger.warn("malformed JSON on stdin", {
        error: errorToObj(err),
        line: line.slice(0, 200)
      });
      return;
    }
    if (isResponse(msg)) {
      this.deliverResponse(msg);
      return;
    }
    if (isRequest(msg)) {
      this.runHandler(msg);
      return;
    }
    if (isNotification(msg)) {
      this.runNotification(msg);
      return;
    }
    this.logger.warn("unrecognized frame shape", {
      line: line.slice(0, 200)
    });
  }
  deliverResponse(msg) {
    if (msg.id == null) {
      this.logger.warn("response with null id; cannot route");
      return;
    }
    const pending = this.pending.get(msg.id);
    if (!pending) {
      this.logger.warn("response for unknown id", { id: String(msg.id) });
      return;
    }
    this.pending.delete(msg.id);
    if ("error" in msg) {
      pending.reject(rpcErrorFrom(msg.error));
    } else {
      pending.resolve(msg.result);
    }
  }
  async runHandler(req) {
    try {
      const result = await this.handler(req.method, req.params);
      const reply = {
        jsonrpc: JSONRPC_VERSION,
        id: req.id,
        result: result ?? null
      };
      await this.writeFrame(reply);
    } catch (err) {
      const rpcErr = err instanceof RPCError ? err : new RPCError(ErrorCodes.InternalError, err instanceof Error ? err.message : String(err));
      const reply = {
        jsonrpc: JSONRPC_VERSION,
        id: req.id,
        error: {
          code: rpcErr.code,
          message: rpcErr.message,
          ...rpcErr.data !== undefined ? { data: rpcErr.data } : {}
        }
      };
      await this.writeFrame(reply);
    }
  }
  async runNotification(n) {
    try {
      await this.handler(n.method, n.params);
    } catch (err) {
      this.logger.warn("notification handler threw", {
        method: n.method,
        error: errorToObj(err)
      });
    }
  }
  async writeFrame(frame) {
    const line = `${JSON.stringify(frame)}
`;
    await this.writer.write(this.encoder.encode(line));
  }
  failPending(err) {
    for (const [id, pending] of this.pending) {
      pending.reject(err);
      this.pending.delete(id);
    }
  }
}
function rpcErrorFrom(e) {
  return new RPCError(e.code, e.message, e.data);
}
function errorToObj(err) {
  if (err instanceof Error) {
    return { name: err.name, message: err.message };
  }
  return { value: String(err) };
}
function processStreams() {
  return {
    input: createStdinStream(),
    output: createStdoutStream()
  };
}
function createStdinStream() {
  const bunGlobal = globalThis.Bun;
  if (bunGlobal?.stdin?.stream) {
    return bunGlobal.stdin.stream();
  }
  const ps = process.stdin;
  return new ReadableStream({
    start(controller) {
      ps.on("data", (chunk) => {
        controller.enqueue(typeof chunk === "string" ? new TextEncoder().encode(chunk) : new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength));
      });
      ps.on("end", () => controller.close());
      ps.on("error", (err) => controller.error(err));
    }
  });
}
function createStdoutStream() {
  const ps = process.stdout;
  return new WritableStream({
    write(chunk) {
      return new Promise((resolve, reject) => {
        const ok = ps.write(chunk, (err) => err ? reject(err) : resolve());
        if (!ok) {
          ps.once("drain", () => resolve());
        }
      });
    }
  });
}

// ../../src/capsule.ts
var PROTOCOL_VERSION = "2025-03-26";
function createCapsule(opts) {
  const tools = new Map;
  for (const t of opts.tools ?? []) {
    if (tools.has(t.name)) {
      throw new Error(`createCapsule: duplicate tool name "${t.name}"`);
    }
    tools.set(t.name, t);
  }
  const streams = opts.streams ?? processStreams();
  let runtimeClient;
  const transport = new Transport({
    streams,
    logger: opts.logger,
    handler: async (method, params) => {
      switch (method) {
        case "initialize":
          return handleInitialize(opts.manifest);
        case "tools/list":
          return handleToolsList(opts.manifest, tools);
        case "tools/call":
          return handleToolsCall(tools, params, runtimeClient);
        case "ping":
          return {};
        case "notifications/initialized":
          return;
        case "shutdown":
          return {};
        default:
          throw new RPCError(ErrorCodes.MethodNotFound, `unknown method: ${method}`);
      }
    }
  });
  runtimeClient = createRuntimeClient(transport);
  return {
    async start() {
      await transport.start();
    },
    async stop() {
      await transport.close();
    },
    get transport() {
      return transport;
    },
    get runtime() {
      return runtimeClient;
    }
  };
}
function handleInitialize(manifest) {
  return {
    protocolVersion: PROTOCOL_VERSION,
    capabilities: {
      tools: { listChanged: false }
    },
    serverInfo: {
      name: manifest.name,
      version: manifest.version
    }
  };
}
function handleToolsList(manifest, tools) {
  manifestToolsCount(manifest);
  const out = [];
  for (const t of tools.values()) {
    out.push({
      name: t.name,
      description: t.description,
      inputSchema: t.inputSchema
    });
  }
  return { tools: out };
}
function manifestToolsCount(m) {
  return (m.tools ?? []).length;
}
async function handleToolsCall(tools, params, runtime) {
  const p = params ?? {};
  const name = p.name;
  if (!name) {
    throw new RPCError(ErrorCodes.InvalidParams, "tools/call: name required");
  }
  const tool = tools.get(name);
  if (!tool) {
    throw new RPCError(ErrorCodes.UnknownTool, `tools/call: unknown tool "${name}"`);
  }
  const ac = new AbortController;
  try {
    const value = await tool.handler(p.arguments, {
      runtime,
      signal: ac.signal
    });
    return toToolResult(value);
  } catch (err) {
    if (err instanceof RPCError) {
      throw err;
    }
    throw new RPCError(ErrorCodes.BackendError, err instanceof Error ? err.message : String(err));
  }
}
// ../../src/tool.ts
function defineTool(tool) {
  return tool;
}
// src/index.ts
var brief = defineTool({
  name: "brief",
  description: "Generate today's brief from recent memory. Returns a short " + "summary plus the threads and entities that informed it.",
  inputSchema: {
    type: "object",
    properties: {
      namespace: { type: "string" },
      thread_limit: { type: "integer", minimum: 1, maximum: 20 },
      entries_per_thread: { type: "integer", minimum: 1, maximum: 50 },
      model_id: { type: "string" }
    },
    additionalProperties: false
  },
  handler: async (input, ctx) => {
    const threadLimit = clamp(input.thread_limit ?? 5, 1, 20);
    const entriesPerThread = clamp(input.entries_per_thread ?? 5, 1, 50);
    const namespace = input.namespace?.trim() ?? "";
    const threadsArgs = { limit: threadLimit };
    if (namespace)
      threadsArgs.namespace = namespace;
    const threadsRes = await ctx.runtime.tools.call("threads.list", threadsArgs);
    const threadsPayload = parseToolJSON(threadsRes);
    const threads = threadsPayload.threads ?? [];
    const entitiesArgs = { limit: 20 };
    if (namespace)
      entitiesArgs.namespace = namespace;
    const entitiesRes = await ctx.runtime.tools.call("entities.list", entitiesArgs);
    const entitiesPayload = parseToolJSON(entitiesRes);
    const entities = entitiesPayload.entities ?? [];
    const threadSummaries = [];
    const contextBlocks = [];
    for (const thread of threads) {
      const entriesRes = await ctx.runtime.tools.call("threads.entries", {
        id: thread.id,
        limit: entriesPerThread
      });
      const entriesPayload = parseToolJSON(entriesRes);
      const refs = entriesPayload.entries ?? [];
      const entryLines = [];
      const participants = new Set;
      for (const ref of refs.slice(0, entriesPerThread)) {
        try {
          const show = await ctx.runtime.tools.call("memory.show", {
            id: `${ref.namespace}:${ref.id}`
          });
          const entryPayload = parseToolJSON(show);
          const md = entryPayload.metadata ?? {};
          const from = stringField(md, "from");
          const subject = stringField(md, "subject");
          if (from)
            participants.add(simplifyAddress(from));
          entryLines.push(formatEntryLine(ref, subject, from));
        } catch {
          entryLines.push(formatEntryLine(ref, "", ""));
        }
      }
      threadSummaries.push({
        id: thread.id,
        subject: thread.subject || "(no subject)",
        namespace: thread.namespace,
        entry_count: thread.entry_count,
        last_seen: thread.last_seen,
        participants: Array.from(participants)
      });
      if (entryLines.length > 0) {
        contextBlocks.push(`### Thread: ${thread.subject || "(no subject)"}
` + entryLines.map((l) => `  - ${l}`).join(`
`));
      }
    }
    const contextText = contextBlocks.length ? contextBlocks.join(`

`) : "(no recent activity)";
    const modelArgs = {
      messages: [
        {
          role: "system",
          content: "You are a brief, calm assistant generating a daily briefing " + "from the user's recent activity. Three short paragraphs: " + "(1) the headline of what's been happening, (2) one or two " + "specific items that need attention, (3) anything quiet " + "that may need a nudge. No bullet lists. No emoji."
        },
        {
          role: "user",
          content: "Here is the user's recent activity. " + `Generate the brief.

` + contextText
        }
      ],
      max_tokens: 600,
      temperature: 0.4
    };
    if (input.model_id)
      modelArgs.model_id = input.model_id;
    let summary = "";
    let inputTokens = 0;
    let outputTokens = 0;
    let degradation;
    try {
      const modelRes = await ctx.runtime.tools.call("model.call", modelArgs);
      if ("isError" in modelRes && modelRes.isError) {
        degradation = "No generation-capable model is configured; the brief " + "contains the read-side facts but no prose summary. " + "Configure a model under Settings \u2192 Models.";
        summary = "(unavailable: no model configured)";
      } else {
        const payload = parseToolJSON(modelRes);
        summary = payload.text ?? "";
        inputTokens = payload.input_tokens ?? 0;
        outputTokens = payload.output_tokens ?? 0;
      }
    } catch (err) {
      degradation = `model.call failed: ${err instanceof Error ? err.message : String(err)}`;
      summary = "(generation failed; see graceful_degradation)";
    }
    const out = {
      generated_at: new Date().toISOString(),
      summary,
      source_notes: `based on ${threads.length} thread(s) and ${entities.length} ` + `entity/entities${namespace ? ` in namespace "${namespace}"` : ""}`,
      threads_considered: threadSummaries,
      entities_seen: entities.slice(0, 10).map((e) => ({
        canonical: e.canonical,
        entry_count: e.entry_count
      }))
    };
    if (inputTokens || outputTokens) {
      out.tokens_used = { input: inputTokens, output: outputTokens };
    }
    if (degradation) {
      out.graceful_degradation = degradation;
    }
    return out;
  }
});
await createCapsule({
  manifest: {
    name: "daily-brief",
    version: "0.1.0",
    author: { name: "Loamss contributors" }
  },
  tools: [brief]
}).start();
function parseToolJSON(res) {
  if (!res.content || res.content.length === 0) {
    throw new Error("tool returned no content");
  }
  const block = res.content[0];
  if (!block?.text) {
    throw new Error("tool returned non-text content");
  }
  return JSON.parse(block.text);
}
function stringField(m, key) {
  const v = m[key];
  return typeof v === "string" ? v : "";
}
function simplifyAddress(s) {
  const angle = s.indexOf("<");
  if (angle > 0)
    return s.slice(0, angle).trim().replace(/^["']|["']$/g, "");
  const at = s.indexOf("@");
  if (at > 0)
    return s.slice(0, at);
  return s.trim();
}
function formatEntryLine(ref, subject, from) {
  const parts = [ref.id];
  if (ref.role)
    parts.push(`[${ref.role}]`);
  if (from)
    parts.push(`from ${simplifyAddress(from)}`);
  if (subject)
    parts.push(`"${subject}"`);
  if (ref.date)
    parts.push(`@ ${ref.date.slice(0, 16).replace("T", " ")}`);
  return parts.join(" ");
}
function clamp(v, lo, hi) {
  if (v < lo)
    return lo;
  if (v > hi)
    return hi;
  return v;
}
