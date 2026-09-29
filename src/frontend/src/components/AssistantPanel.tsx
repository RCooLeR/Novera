import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Bot, Check, CheckCircle2, Circle, Copy, Loader2, RefreshCw, Send, Settings2, ShieldCheck, Square, Trash2, User, Wrench, X } from "lucide-react";
import { useStore } from "../state/store";
import type { ChatMsg } from "../state/store";
import Splitter from "./Splitter";
import { visibleAssistantContent } from "./assistantContent";
import { approvalIntentPages } from "./approvalIntent";
import { writeClipboardText } from "../lib/clipboard";
import ConfirmModal from "./ConfirmModal";
import { toolCardPresentation } from "./toolCardPresentation";

const DEFAULT_REQUEST_TIMEOUT_SEC = 1800;
const MAX_REQUEST_TIMEOUT_SEC = 21600;

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

function ApprovalIntentDetail({ msg }: { msg: ChatMsg }) {
  const pages = useMemo(() => approvalIntentPages(msg.intent ?? ""), [msg.intent]);
  const [page, setPage] = useState(0);
  useEffect(() => setPage(0), [msg.intent]);
  const currentPage = Math.min(page, pages.length - 1);

  if (!msg.intent || !msg.intentDigest) {
    return <div className="toolcard__intenterror">This request has no verifiable canonical intent and cannot be approved.</div>;
  }

  return (
    <div className="toolcard__intent">
      <div className="toolcard__intentmeta">
        <span>
          Complete canonical intent · page {currentPage + 1} of {pages.length}
        </span>
        {msg.expiresAt && <span>Expires {new Date(msg.expiresAt).toLocaleString()}</span>}
      </div>
      <pre className="toolcard__detail" aria-label={`Canonical approval intent, page ${currentPage + 1} of ${pages.length}`}>
        {pages[currentPage]}
      </pre>
      {pages.length > 1 && (
        <div className="toolcard__intentpages">
          <button type="button" className="btn" disabled={currentPage === 0} onClick={() => setPage((value) => Math.max(0, value - 1))}>
            Previous
          </button>
          <button
            type="button"
            className="btn"
            disabled={currentPage === pages.length - 1}
            onClick={() => setPage((value) => Math.min(pages.length - 1, value + 1))}
          >
            Next
          </button>
        </div>
      )}
      <div className="toolcard__digest">
        SHA-256 <code>{msg.intentDigest}</code>
      </div>
    </div>
  );
}

function ToolCard({ msg }: { msg: ChatMsg }) {
  const approveAgent = useStore((s) => s.approveAgent);
  const toolName = msg.tool ?? "tool";
  const { summary, prompt, detail, argumentError, approvalSafe } = toolCardPresentation(toolName, msg.args);

  return (
    <div className="toolcard">
      <div className="toolcard__head">
        <Wrench size={13} />
        <span className="toolcard__name">{toolName}</span>
        <span className="toolcard__arg">{summary}</span>
      </div>
      {argumentError && (
        <div className="toolcard__intenterror" role="alert">
          {argumentError}
        </div>
      )}
      {msg.approval === "pending" && msg.callId && (
        <div className="toolcard__approve">
          <span className={toolName === "run_command" || toolName === "http_request" ? "toolcard__warn" : ""}>{prompt}</span>
          {detail && <pre className="toolcard__detail">{detail}</pre>}
          <ApprovalIntentDetail msg={msg} />
          <div className="toolcard__approvebtns">
            <button
              type="button"
              className="btn btn--primary"
              disabled={!approvalSafe || !msg.intent || !msg.intentDigest}
              onClick={() => void approveAgent(msg.callId!, true, msg.intentDigest)}
            >
              <Check size={13} /> Allow
            </button>
            <button
              type="button"
              className="btn"
              disabled={!msg.intent || !msg.intentDigest}
              onClick={() => void approveAgent(msg.callId!, false, msg.intentDigest)}
            >
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
  const settingsError = useStore((s) => s.settingsError);
  const workspaceTransitioning = useStore((s) => s.workspaceTransitioning);
  const llmAPIKeyAvailable = useStore((s) => s.llmAPIKeyAvailable);
  const llmAPIKeyStatus = useStore((s) => s.llmAPIKeyStatus);
  const llmAPIKeyStatusMessage = useStore((s) => s.llmAPIKeyStatusMessage);
  const agentMode = useStore((s) => s.agentMode);
  const agentPlan = useStore((s) => s.agentPlan);
  const toggleAgentMode = useStore((s) => s.toggleAgentMode);
  const resizeAssistant = useStore((s) => s.resizeAssistant);
  const assistantWidth = useStore((s) => s.assistantWidth);
  const setStatus = useStore((s) => s.setStatus);
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
  const [confirmRemoveKey, setConfirmRemoveKey] = useState(false);
  const [streamAnnouncement, setStreamAnnouncement] = useState("");
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const stickToBottomRef = useRef(true);
  const wasStreamingRef = useRef(false);

  const llm = settings?.llm;
  const llmCredentialPending = llmAPIKeyStatus === "quarantined";
  const llmCredentialUnavailable = llmAPIKeyStatus === "unavailable";
  const settingsBlocked = Boolean(settingsError);
  const settingsDiagnosticId = settingsError ? "assistant-settings-error" : undefined;

  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickToBottomRef.current) el.scrollTop = el.scrollHeight;
  }, [chat, agentPlan]);

  useEffect(() => {
    if (chatStreaming && !wasStreamingRef.current) setStreamAnnouncement("Assistant response started.");
    if (!chatStreaming && wasStreamingRef.current) setStreamAnnouncement("Assistant response finished.");
    wasStreamingRef.current = chatStreaming;
  }, [chatStreaming]);

  const handleMessagesScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    stickToBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  };

  const submit = () => {
    const t = input;
    if (!t.trim() || chatStreaming || workspaceTransitioning) return;
    stickToBottomRef.current = true;
    setInput("");
    if (agentMode) void sendAgent(t);
    else void sendChat(t);
  };

  return (
    <div className="assistant">
      <Splitter axis="x" side="left" onResize={resizeAssistant} value={assistantWidth} min={260} max={720} label="Resize assistant" />
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
          disabled={settingsBlocked}
          aria-describedby={settingsDiagnosticId}
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

      {settingsError && (
        <div id="assistant-settings-error" className="assistant__config-error" role="alert">
          {settingsError}
        </div>
      )}

      {configOpen && llm && (
        <div className="assistant__config">
          <label>
            Provider
            <select
              value={llm.provider}
              disabled={settingsBlocked}
              aria-describedby={settingsDiagnosticId}
              onChange={(e) => void saveLLMConfig({ provider: e.target.value })}
            >
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
              disabled={settingsBlocked}
              aria-describedby={settingsDiagnosticId}
              spellCheck={false}
              onBlur={(e) => {
                if (e.target.value !== llm.baseURL) void saveLLMConfig({ baseURL: e.target.value });
              }}
              placeholder="http://localhost:11434/v1"
            />
          </label>
          <label>
            API key {llmAPIKeyAvailable ? <span className="assistant__keyset">stored</span> : null}
            {llmCredentialPending ? <span className="assistant__keypending">re-entry required</span> : null}
            {llmCredentialUnavailable ? <span className="assistant__keypending">unavailable</span> : null}
            <input
              type="password"
              value={keyInput}
              disabled={settingsBlocked || llmCredentialUnavailable}
              aria-describedby={settingsDiagnosticId}
              spellCheck={false}
              onChange={(e) => setKeyInput(e.target.value)}
              placeholder={
                llmAPIKeyAvailable
                  ? "•••••• (stored — type to replace)"
                  : llmCredentialPending
                    ? "Re-enter the key for this provider origin"
                    : llmCredentialUnavailable
                      ? "Credential storage is unavailable"
                      : "Enter an API key if this provider requires one"
              }
              onBlur={async () => {
                if (keyInput && !settingsBlocked) {
                  if (await setApiKey(keyInput)) setKeyInput("");
                }
              }}
            />
            {llmCredentialUnavailable && llmAPIKeyStatusMessage ? (
              <span className="assistant__keyerror" role="alert">
                {llmAPIKeyStatusMessage}
              </span>
            ) : null}
          </label>
          {llmAPIKeyAvailable && !llmCredentialUnavailable && (
            <button
              type="button"
              className="btn btn--danger"
              disabled={settingsBlocked}
              aria-describedby={settingsDiagnosticId}
              onClick={() => setConfirmRemoveKey(true)}
            >
              Remove stored key
            </button>
          )}
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
              disabled={settingsBlocked}
              aria-describedby={settingsDiagnosticId}
              spellCheck={false}
              onBlur={(e) => {
                const n = Math.round(Number(e.target.value));
                const v = Number.isFinite(n) && n > 0 ? Math.max(30, Math.min(MAX_REQUEST_TIMEOUT_SEC, n)) : DEFAULT_REQUEST_TIMEOUT_SEC;
                if (v !== llm.requestTimeoutSec) void saveLLMConfig({ requestTimeoutSec: v });
              }}
            />
          </label>
          <button className="btn" disabled={settingsBlocked} aria-describedby={settingsDiagnosticId} onClick={() => void loadModels()}>
            <RefreshCw size={14} /> Load models
          </button>
        </div>
      )}

      <span className="sr-only" role="status" aria-live="polite">{streamAnnouncement}</span>
      <div className="assistant__messages" ref={scrollRef} aria-busy={chatStreaming} onScroll={handleMessagesScroll}>
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
                  onClick={() =>
                    void writeClipboardText(visibleAssistantContent(m.content))
                      .then(() => setStatus("Copied assistant message.", "success"))
                      .catch((error) => setStatus(error instanceof Error ? error.message : String(error), "error"))
                  }
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
          disabled={chatStreaming || workspaceTransitioning}
          placeholder={
            workspaceTransitioning
              ? "Switching workspaces…"
              : chatStreaming
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
            disabled={workspaceTransitioning || !input.trim()}
            title="Send (Enter)"
          >
            <Send size={14} />
          </button>
        )}
      </div>
      {confirmRemoveKey && (
        <ConfirmModal
          title="Remove stored API key?"
          body="The key will be deleted from credential storage. The provider and model settings will be kept."
          confirmLabel="Remove key"
          danger
          onCancel={() => setConfirmRemoveKey(false)}
          onConfirm={() => {
            void setApiKey("").then((removed) => {
              if (removed) setConfirmRemoveKey(false);
            });
          }}
        />
      )}
    </div>
  );
}
