import { agentConfigFromSettings, useStore } from "../state/store";

export default function SettingsView() {
  const settings = useStore((s) => s.settings);
  const saveEditorConfig = useStore((s) => s.saveEditorConfig);
  const saveAgentConfig = useStore((s) => s.saveAgentConfig);
  const setUIFontSize = useStore((s) => s.setUIFontSize);
  const saveLLMConfig = useStore((s) => s.saveLLMConfig);
  const setApiKey = useStore((s) => s.setApiKey);
  const loadModels = useStore((s) => s.loadModels);
  const models = useStore((s) => s.models);

  if (!settings) return <div className="settingsv__empty">Loading…</div>;
  const ed = settings.editor;
  const llm = settings.llm;
  const agent = agentConfigFromSettings(settings);

  return (
    <div className="settingsv">
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
            onChange={(e) => {
              const provider = e.target.value;
              void saveLLMConfig({ provider });
              // Local Ollama needs no key — clear any stored one so it can't be
              // sent to a provider that ignores it (and the "stored" badge clears).
              if (provider === "ollama") void setApiKey("");
            }}
          >
            <option value="ollama">Ollama (local)</option>
            <option value="openai">OpenAI</option>
            <option value="custom">Custom (OpenAI-compatible)</option>
          </select>
        </label>
        <label className="settingsv__row settingsv__col">
          <span>Base URL</span>
          <input value={llm.baseURL} spellCheck={false} onChange={(e) => void saveLLMConfig({ baseURL: e.target.value })} />
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
          <span>API key {llm.apiKeyRef ? <em className="settingsv__set">stored</em> : null}</span>
          <input
            type="password"
            spellCheck={false}
            placeholder={llm.apiKeyRef ? "•••••• (Enter to save, or type to replace)" : "Not needed for local Ollama"}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                const el = e.currentTarget;
                if (el.value) {
                  void setApiKey(el.value);
                  el.value = "";
                  el.blur();
                }
              }
            }}
            onBlur={(e) => {
              if (e.target.value) {
                void setApiKey(e.target.value);
                e.target.value = "";
              }
            }}
          />
        </label>
        )}
        <button className="btn" onClick={() => void loadModels()}>
          Load models
        </button>
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
    </div>
  );
}
