import { useState } from "react";
import { agentConfigFromSettings, useStore } from "../state/store";
import ConfirmModal from "./ConfirmModal";

export default function SettingsView() {
  const settings = useStore((s) => s.settings);
  const settingsError = useStore((s) => s.settingsError);
  const llmAPIKeyAvailable = useStore((s) => s.llmAPIKeyAvailable);
  const llmAPIKeyStatus = useStore((s) => s.llmAPIKeyStatus);
  const llmAPIKeyStatusMessage = useStore((s) => s.llmAPIKeyStatusMessage);
  const saveEditorConfig = useStore((s) => s.saveEditorConfig);
  const saveAgentConfig = useStore((s) => s.saveAgentConfig);
  const setUIFontSize = useStore((s) => s.setUIFontSize);
  const saveLLMConfig = useStore((s) => s.saveLLMConfig);
  const setApiKey = useStore((s) => s.setApiKey);
  const loadModels = useStore((s) => s.loadModels);
  const models = useStore((s) => s.models);
  const [confirmRemoveKey, setConfirmRemoveKey] = useState(false);

  if (!settings) return <div className="settingsv__empty">Loading…</div>;
  const ed = settings.editor;
  const llm = settings.llm;
  const agent = agentConfigFromSettings(settings);
  const llmCredentialPending = llmAPIKeyStatus === "quarantined";
  const llmCredentialUnavailable = llmAPIKeyStatus === "unavailable";

  return (
    <div className="settingsv">
      {settingsError && (
        <div className="settingsv__error" role="alert">
          {settingsError}
        </div>
      )}
      <fieldset className="settingsv__fields" disabled={Boolean(settingsError)}>
      <section className="settingsv__group">
        <h3>Editor</h3>
        <label className="settingsv__row">
          <span>Font size</span>
          {/* Commit on blur (not per keystroke); the store clamps. key resets the
              uncontrolled input to the clamped value after commit. */}
          <input
            key={`fs-${ed.fontSize}`}
            type="number"
            min={9}
            max={28}
            defaultValue={ed.fontSize}
            onBlur={(e) => void saveEditorConfig({ fontSize: Number(e.target.value) || 13 })}
          />
        </label>
        <label className="settingsv__row">
          <span>Tab size</span>
          <input
            key={`ts-${ed.tabSize}`}
            type="number"
            min={1}
            max={8}
            defaultValue={ed.tabSize}
            onBlur={(e) => void saveEditorConfig({ tabSize: Number(e.target.value) || 4 })}
          />
        </label>
        <label className="settingsv__row settingsv__check">
          <input type="checkbox" checked={ed.wordWrap} onChange={(e) => void saveEditorConfig({ wordWrap: e.target.checked })} />
          <span>Word wrap</span>
        </label>
        <label className="settingsv__row settingsv__check">
          <input type="checkbox" checked={ed.minimap} onChange={(e) => void saveEditorConfig({ minimap: e.target.checked })} />
          <span>Minimap</span>
        </label>
        <label className="settingsv__row settingsv__check">
          <input
            type="checkbox"
            checked={ed.formatOnSave}
            onChange={(e) => void saveEditorConfig({ formatOnSave: e.target.checked })}
          />
          <span>Format on save</span>
        </label>
      </section>

      <section className="settingsv__group">
        <h3>AI provider</h3>
        <label className="settingsv__row">
          <span>Provider</span>
          <select
            value={llm.provider}
            onChange={(e) => void saveLLMConfig({ provider: e.target.value })}
          >
            <option value="ollama">Ollama (local)</option>
            <option value="openai">OpenAI</option>
            <option value="custom">Custom (OpenAI-compatible)</option>
          </select>
        </label>
        <label className="settingsv__row settingsv__col">
          <span>Base URL</span>
          {/* An endpoint-origin change deliberately detaches its stored API key.
              Commit once on blur so intermediate keystrokes cannot persist a
              partial origin and delete the credential while the user types. */}
          <input
            key={`settings-baseurl-${llm.baseURL}`}
            defaultValue={llm.baseURL}
            spellCheck={false}
            onBlur={(e) => {
              if (e.currentTarget.value !== llm.baseURL) void saveLLMConfig({ baseURL: e.currentTarget.value });
            }}
          />
        </label>
        <label className="settingsv__row settingsv__col">
          <span>Model</span>
          <select value={llm.model} onChange={(e) => void saveLLMConfig({ model: e.target.value })}>
            {!llm.model && <option value="">No model</option>}
            {llm.model && !models.includes(llm.model) && <option value={llm.model}>{llm.model}</option>}
            {models.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        {llm.provider !== "ollama" && (
        <label className="settingsv__row settingsv__col">
          <span>
            API key {llmAPIKeyAvailable ? <em className="settingsv__set">stored</em> : null}
            {llmCredentialPending ? <em className="settingsv__pending">re-entry required</em> : null}
            {llmCredentialUnavailable ? <em className="settingsv__pending">unavailable</em> : null}
          </span>
          <input
            type="password"
            disabled={llmCredentialUnavailable}
            spellCheck={false}
            placeholder={
              llmAPIKeyAvailable
                ? "•••••• (Enter to save, or type to replace)"
                : llmCredentialPending
                  ? "Re-enter the key for this provider origin"
                  : llmCredentialUnavailable
                    ? "Credential storage is unavailable"
                    : "Enter an API key if this provider requires one"
            }
            onKeyDown={async (e) => {
              if (e.key === "Enter") {
                const el = e.currentTarget;
                if (el.value) {
                  const value = el.value;
                  if (await setApiKey(value)) {
                    el.value = "";
                    el.blur();
                  }
                }
              }
            }}
            onBlur={async (e) => {
              if (e.target.value) {
                const el = e.currentTarget;
                const value = el.value;
                if (await setApiKey(value)) el.value = "";
              }
            }}
          />
          {llmCredentialUnavailable && llmAPIKeyStatusMessage ? (
            <span className="settingsv__note" role="alert">
              {llmAPIKeyStatusMessage}
            </span>
          ) : null}
        </label>
        )}
        <div className="settingsv__actions">
          <button type="button" className="btn" onClick={() => void loadModels()}>
            Load models
          </button>
          {llmAPIKeyAvailable && !llmCredentialUnavailable && (
            <button type="button" className="btn btn--danger" onClick={() => setConfirmRemoveKey(true)}>
              Remove stored key
            </button>
          )}
        </div>
      </section>

      <section className="settingsv__group">
        <h3>Agent runtime</h3>
        <label className="settingsv__row">
          <span>Tool output chars</span>
          <input
            key={`agent-output-${agent.maxToolOutputChars}`}
            type="number"
            min={1000}
            max={50000}
            step={500}
            defaultValue={agent.maxToolOutputChars}
            onBlur={(e) => void saveAgentConfig({ maxToolOutputChars: Number(e.target.value) || 6000 })}
          />
        </label>
        <label className="settingsv__row">
          <span>Step batch</span>
          <input
            key={`agent-batch-${agent.stepBatch}`}
            type="number"
            min={1}
            max={500}
            defaultValue={agent.stepBatch}
            onBlur={(e) => void saveAgentConfig({ stepBatch: Number(e.target.value) || 50 })}
          />
        </label>
        <label className="settingsv__row">
          <span>Max steps</span>
          <input
            key={`agent-steps-${agent.maxTotalSteps}`}
            type="number"
            min={1}
            max={10000}
            defaultValue={agent.maxTotalSteps}
            onBlur={(e) => void saveAgentConfig({ maxTotalSteps: Number(e.target.value) || 1000 })}
          />
        </label>
        <label className="settingsv__row">
          <span>History window</span>
          <input
            key={`agent-history-${agent.historyWindowGroups}`}
            type="number"
            min={1}
            max={50}
            defaultValue={agent.historyWindowGroups}
            onBlur={(e) => void saveAgentConfig({ historyWindowGroups: Number(e.target.value) || 8 })}
          />
        </label>
        <label className="settingsv__row">
          <span>Command timeout</span>
          <input
            key={`agent-cmd-${agent.commandTimeoutSec}`}
            type="number"
            min={5}
            max={3600}
            defaultValue={agent.commandTimeoutSec}
            onBlur={(e) => void saveAgentConfig({ commandTimeoutSec: Number(e.target.value) || 60 })}
          />
        </label>
        <div className="settingsv__note">Tuning for slow local models and long-running agent work.</div>
      </section>

      <section className="settingsv__group">
        <h3>Interface</h3>
        <label className="settingsv__row">
          <span>UI font size</span>
          <input
            key={`uifs-${settings.uiFontSize || 13}`}
            type="number"
            min={10}
            max={20}
            defaultValue={settings.uiFontSize || 13}
            onBlur={(e) => void setUIFontSize(Number(e.target.value) || 13)}
          />
        </label>
        <div className="settingsv__note">Scales sidebars, tabs, and panels. Dark theme (more themes coming).</div>
      </section>
      </fieldset>
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
