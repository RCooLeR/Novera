import { useEffect, useRef, useState, type ReactNode } from "react";
import { Bot, Check, CheckCircle2, Circle, Copy, Loader2, RefreshCw, Send, Settings2, ShieldCheck, Square, Trash2, User, Wrench, X } from "lucide-react";
import { useStore } from "../state/store";
import type { ChatMsg } from "../state/store";
import Splitter from "./Splitter";

const DEFAULT_REQUEST_TIMEOUT_SEC = 1800;
const MAX_REQUEST_TIMEOUT_SEC = 21600;

function visibleAssistantContent(content: string): string {
  let text = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").trim();
  if (!text) return "";
  const markerIndexes = ["<channel|>", "<|channel", "<|thought", "\nthought\n", "\nanalysis\n"]
    .map((marker) => text.indexOf(marker))
    .filter((idx) => idx >= 0);
  let cut = markerIndexes.length ? Math.min(...markerIndexes) : -1;
  let offset = 0;
  for (const line of text.match(/[^\n]*(?:\n|$)/g) ?? []) {
    if (line === "") break;
    if (isInternalAssistantLine(line.trim())) {
      cut = cut < 0 ? offset : Math.min(cut, offset);
      break;
    }
    offset += line.length;
  }
  if (cut >= 0) text = text.slice(0, cut);
  return text.trim();
}

function isInternalAssistantLine(line: string): boolean {
  const lower = line.toLowerCase();
  return (
    lower === "thought" ||
    lower === "analysis" ||
    lower.startsWith("the user denied the write_file request") ||
    lower.startsWith("the user denied the append_file request") ||
    lower.startsWith("the write_file was rejected") ||
    lower.startsWith("since write_file was denied") ||
    lower.startsWith("wait, looking at the previous turn") ||
    lower.startsWith("wait, i see what happened") ||
    lower.startsWith("wait, i see the instruction") ||
    lower.startsWith("actually, looking at the error") ||
    lower.startsWith("actually, let me try") ||
    lower.startsWith("let me try append_file") ||
    lower.includes("continue now by emitting real tool_calls") ||
    (lower.startsWith("the prompt says") && lower.includes("tool"))
  );
}

// Minimal, dependency-free, XSS-safe markdown for assistant output: fenced code
// blocks and inline `code` rendered as real elements, everything else as plain
// React text nodes (never raw HTML), so model output can't inject markup.
function renderInline(text: string): ReactNode[] {
  return text.split(/`/).map((s, i) =>
    i % 2 === 1 ? (
      <code key={i} className="md-inline">
        {s}
      </code>
    ) : (
      <span key={i}>{s}</span>
    ),
  );
}

function MessageBody({ content }: { content: string }) {
  const parts = visibleAssistantContent(content).split(/```/);
  return (
    <>
      {parts.map((part, i) => {
        if (i % 2 === 1) {
          // Fenced block: drop an optional language label on the first line.
          const nl = part.indexOf("\n");
          const code = (nl >= 0 ? part.slice(nl + 1) : part).replace(/\n$/, "");
          return (
            <pre key={i} className="md-code">
              <code>{code}</code>
            </pre>
          );
        }
        return <span key={i}>{renderInline(part)}</span>;
      })}
    </>
  );
}

function ToolCard({ msg }: { msg: ChatMsg }) {
  const approveAgent = useStore((s) => s.approveAgent);
  let args: Record<string, string> = {};
  try {
    args = JSON.parse(msg.args || "{}");
  } catch {
    /* keep empty */
  }
  const toolName = msg.tool ?? "tool";
  const summary =
    args.url ??
    args.command ??
    args.path ??
    args.outPath ??
    args.connectionId ??
    args.table ??
    args.sql ??
    (args.query ? `"${args.query}"` : "");

  // Tool-specific approval prompt + the exact thing being approved.
  let prompt = "Allow this action?";
  let detail: string | null = null;
  if (toolName === "run_command") {
    prompt = "Run this shell command in the workspace?";
    detail = args.command ?? "";
  } else if (toolName === "http_request") {
    prompt = "Send this HTTP request?";
    detail = `${args.method || "GET"} ${args.url || ""}`;
    if (args.body) detail += `\n\n${args.body.slice(0, 1200)}`;
  } else if (toolName === "db_query") {
    prompt = "Run this database query?";
    detail = args.sql ?? "";
  } else if (toolName === "write_file") {
    prompt = `Create or overwrite ${args.path}?`;
    detail = (args.content ?? "").slice(0, 1200);
  } else if (toolName === "apply_edit") {
    prompt = `Apply this edit to ${args.path}?`;
    detail = `- ${(args.oldText ?? "").slice(0, 600)}\n+ ${(args.newText ?? "").slice(0, 600)}`;
  }

  return (
    <div className="toolcard">
      <div className="toolcard__head">
        <Wrench size={13} />
        <span className="toolcard__name">{toolName}</span>
        <span className="toolcard__arg">{String(summary)}</span>
      </div>
      {msg.approval === "pending" && msg.callId && (
        <div className="toolcard__approve">
          <span className={toolName === "run_command" || toolName === "http_request" ? "toolcard__warn" : ""}>{prompt}</span>
          {detail && <pre className="toolcard__detail">{detail}</pre>}
          <div className="toolcard__approvebtns">
            <button className="btn btn--primary" onClick={() => void approveAgent(msg.callId!, true)}>
              <Check size={13} /> Allow
            </button>
            <button className="btn" onClick={() => void approveAgent(msg.callId!, false)}>
              <X size={13} /> Deny
            </button>
          </div>
        </div>
      )}
      {msg.result != null && <div className="toolcard__status">Completed</div>}
    </div>
  );
}

function ContinueCard({ msg }: { msg: ChatMsg }) {
  const approveAgent = useStore((s) => s.approveAgent);
  const steps = msg.content || "";
  if (msg.approval !== "pending" || !msg.callId) {
    return (
      <div className="continuecard continuecard--resolved">
        {msg.approval === "approved" ? `Continued past ${steps} steps.` : `Stopped at ${steps} steps.`}
      </div>
    );
  }
  return (
    <div className="continuecard">
      <div className="continuecard__text">
        The agent has run <b>{steps}</b> steps and isn't finished. Keep going?
      </div>
      <div className="continuecard__btns">
        <button className="btn btn--primary" onClick={() => void approveAgent(msg.callId!, true)}>
          <Check size={13} /> Continue
        </button>
        <button className="btn" onClick={() => void approveAgent(msg.callId!, false)}>
          <Square size={13} /> Stop
        </button>
      </div>
    </div>
  );
}

export default function AssistantPanel() {
  const chat = useStore((s) => s.chat);
  const chatStreaming = useStore((s) => s.chatStreaming);
  const models = useStore((s) => s.models);
  const settings = useStore((s) => s.settings);
  const agentMode = useStore((s) => s.agentMode);
  const agentPlan = useStore((s) => s.agentPlan);
  const toggleAgentMode = useStore((s) => s.toggleAgentMode);
  const resizeAssistant = useStore((s) => s.resizeAssistant);
  const sendChat = useStore((s) => s.sendChat);
  const sendAgent = useStore((s) => s.sendAgent);
  const cancelChat = useStore((s) => s.cancelChat);
  const clearChat = useStore((s) => s.clearChat);
  const loadModels = useStore((s) => s.loadModels);
  const openAuditLog = useStore((s) => s.openAuditLog);
  const saveLLMConfig = useStore((s) => s.saveLLMConfig);
  const setApiKey = useStore((s) => s.setApiKey);

  const [input, setInput] = useState("");
  const [configOpen, setConfigOpen] = useState(false);
  const [keyInput, setKeyInput] = useState("");
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const stickToBottomRef = useRef(true);

  const llm = settings?.llm;

  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickToBottomRef.current) el.scrollTop = el.scrollHeight;
  }, [chat, agentPlan]);

  const handleMessagesScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    stickToBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  };

  const submit = () => {
    const t = input;
    if (!t.trim() || chatStreaming) return;
    stickToBottomRef.current = true;
    setInput("");
    if (agentMode) void sendAgent(t);
    else void sendChat(t);
  };

  return (
    <div className="assistant">
      <Splitter axis="x" side="left" onResize={resizeAssistant} />
      <div className="assistant__header">
        <div className="assistant__modes">
          <button
            className={`assistant__mode ${!agentMode ? "active" : ""}`}
            onClick={() => {
              if (agentMode) toggleAgentMode();
            }}
          >
            Ask
          </button>
          <button
            className={`assistant__mode ${agentMode ? "active" : ""}`}
            onClick={() => {
              if (!agentMode) toggleAgentMode();
            }}
          >
            Agent
          </button>
        </div>
        <span className="assistant__spacer" />
        <select
          className="assistant__model"
          value={llm?.model ?? ""}
          onChange={(e) => void saveLLMConfig({ model: e.target.value })}
          title="Model"
        >
          {!llm?.model && models.length === 0 && <option value="">No model</option>}
          {llm?.model && !models.includes(llm.model) && <option value={llm.model}>{llm.model}</option>}
          {models.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
        <button className="icon-btn" title="Provider settings" onClick={() => setConfigOpen((o) => !o)}>
          <Settings2 size={15} />
        </button>
        <button className="icon-btn" title="Agent audit log" onClick={() => void openAuditLog()}>
          <ShieldCheck size={15} />
        </button>
        <button className="icon-btn" title="Clear conversation" onClick={clearChat}>
          <Trash2 size={15} />
        </button>
      </div>

      {configOpen && llm && (
        <div className="assistant__config">
          <label>
            Provider
            <select value={llm.provider} onChange={(e) => void saveLLMConfig({ provider: e.target.value })}>
              <option value="ollama">Ollama (local)</option>
              <option value="openai">OpenAI</option>
              <option value="custom">Custom (OpenAI-compatible)</option>
            </select>
          </label>
          <label>
            Base URL
            {/* Commit on blur (uncontrolled, key resets to the saved value) rather
                than persisting a partial URL on every keystroke. */}
            <input
              key={`baseurl-${llm.baseURL}`}
              defaultValue={llm.baseURL}
              spellCheck={false}
              onBlur={(e) => {
                if (e.target.value !== llm.baseURL) void saveLLMConfig({ baseURL: e.target.value });
              }}
              placeholder="http://localhost:11434/v1"
            />
          </label>
          <label>
            API key {llm.apiKeyRef ? <span className="assistant__keyset">stored</span> : null}
            <input
              type="password"
              value={keyInput}
              spellCheck={false}
              onChange={(e) => setKeyInput(e.target.value)}
              placeholder={llm.apiKeyRef ? "•••••• (stored — type to replace)" : "Not needed for local Ollama"}
              onBlur={() => {
                if (keyInput) {
                  void setApiKey(keyInput);
                  setKeyInput("");
                }
              }}
            />
          </label>
          <label>
            Request timeout (seconds)
            {/* Commit on blur (uncontrolled, key resets to the saved value). A
                local model's first call also pays model-load time, so this must
                be generous; raise it if you see "context deadline exceeded". */}
            <input
              key={`timeout-${llm.requestTimeoutSec}`}
              type="number"
              min={30}
              max={MAX_REQUEST_TIMEOUT_SEC}
              defaultValue={llm.requestTimeoutSec || DEFAULT_REQUEST_TIMEOUT_SEC}
              spellCheck={false}
              onBlur={(e) => {
                const n = Math.round(Number(e.target.value));
                const v = Number.isFinite(n) && n > 0 ? Math.max(30, Math.min(MAX_REQUEST_TIMEOUT_SEC, n)) : DEFAULT_REQUEST_TIMEOUT_SEC;
                if (v !== llm.requestTimeoutSec) void saveLLMConfig({ requestTimeoutSec: v });
              }}
            />
          </label>
          <button className="btn" onClick={() => void loadModels()}>
            <RefreshCw size={14} /> Load models
          </button>
        </div>
      )}

      <div className="assistant__messages" ref={scrollRef} onScroll={handleMessagesScroll}>
        {chat.length === 0 && (
          <div className="assistant__empty">
            {agentMode
              ? "Agent mode: I can read, search, and (with your approval) edit files in this workspace."
              : "Ask about the open file or your project. The active file is sent along as context."}
          </div>
        )}
        {chat.map((m) =>
          m.role === "tool" ? (
            m.continuePrompt ? <ContinueCard key={m.id} msg={m} /> : <ToolCard key={m.id} msg={m} />
          ) : (
            <div key={m.id} className={`msg msg--${m.role} ${m.error ? "msg--error" : ""}`}>
              <div className="msg__avatar">{m.role === "user" ? <User size={14} /> : <Bot size={14} />}</div>
              <div className="msg__body">
                {m.role === "assistant" ? <MessageBody content={m.content} /> : m.content}
                {m.streaming && <span className="msg__cursor">▍</span>}
              </div>
              {m.role === "assistant" && !m.streaming && visibleAssistantContent(m.content) && (
                <button
                  className="msg__copy"
                  title="Copy message"
                  onClick={() => void navigator.clipboard?.writeText(visibleAssistantContent(m.content))}
                >
                  <Copy size={12} />
                </button>
              )}
            </div>
          ),
        )}
      </div>

      {agentMode && agentPlan.length > 0 && (
        <div className="assistant__plan">
          <div className="assistant__plan-title">
            Plan
            <span className="assistant__plan-count">
              {agentPlan.filter((s) => s.status === "done").length}/{agentPlan.length}
            </span>
          </div>
          <ul className="plan">
            {agentPlan.map((step, i) => (
              <li key={i} className={`plan__step plan__step--${step.status}`}>
                <span className="plan__icon">
                  {step.status === "done" ? (
                    <CheckCircle2 size={14} />
                  ) : step.status === "in_progress" ? (
                    <Loader2 size={14} className="spin" />
                  ) : (
                    <Circle size={14} />
                  )}
                </span>
                <span className="plan__text">{step.title}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="assistant__input">
        <textarea
          value={input}
          disabled={chatStreaming}
          placeholder={
            chatStreaming
              ? "Stop the current response to send another message…"
              : "Ask Novera…  (Enter to send, Shift+Enter for newline)"
          }
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              submit();
            }
          }}
        />
        {chatStreaming ? (
          <button className="btn btn--primary assistant__send" onClick={cancelChat} title="Stop">
            <Square size={14} />
          </button>
        ) : (
          <button
            className="btn btn--primary assistant__send"
            onClick={submit}
            disabled={!input.trim()}
            title="Send (Enter)"
          >
            <Send size={14} />
          </button>
        )}
      </div>
    </div>
  );
}
