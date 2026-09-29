import { useEffect, useRef, useState } from "react";

/* ---------------------------------------------------------------------------
 * BootSplash — Novera "JARVIS" cognition-core loader.
 *
 * Ported from the standalone novera_jarvis_loader (index.html / styles.css /
 * app.js) into the React/Wails shell: the abstract AI-core background is a
 * bundled image, the orbital motion / particle plexus / scan beam is drawn on
 * a <canvas>, and the HUD (title, AI Core Status, system log, progress) is
 * React-rendered from a single `progress` value. When progress reaches 100%
 * it holds briefly, fades the whole overlay out, and calls onDone().
 *
 * Self-contained — assets live in /public, so it's CSP-safe.
 * ------------------------------------------------------------------------- */

// The bar ramps toward CAP, then holds there until the app reports ready
// (so the splash genuinely covers startup); once ready + MIN_MS elapsed it
// finishes to 100%, holds, and fades out.
const RAMP_MS = 3200; // time to ease up to CAP
const MIN_MS = 2400; // minimum on-screen time before we allow the finish
const CAP = 0.92; // ceiling held until the app is ready
const FINISH_MS = 600; // smooth CAP → 100% once ready
const HOLD_MS = 480; // dwell at 100% before fading out
const FADE_MS = 640; // must match the .leaving transition in splash.css

const LOGS = [
  "> Boot sequence initiated",
  "> Checking integrity... OK",
  "> Loading core modules... OK",
  "> Initializing AI core...",
  "> Synchronizing neural pathways...",
  "> Establishing secure channel... OK",
  "> System ready",
];

const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3);

function stateFor(t: number): string {
  if (t < 0.15) return "Booting";
  if (t < 0.36) return "Loading";
  if (t < 0.68) return "Initializing";
  if (t < 0.91) return "Synchronizing";
  if (t < 1) return "Finalizing";
  return "Ready";
}

type RGB = [number, number, number];
type Orbiter = { rx: number; ry: number; speed: number; angle: number; color: RGB; size: number };
type Particle = { a: number; r: number; wobble: number; phase: number; color: RGB };

export default function BootSplash({ onDone, ready }: { onDone: () => void; ready: boolean }) {
  const splashRef = useRef<HTMLDivElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [prog, setProg] = useState(0.01);
  const [leaving, setLeaving] = useState(false);
  const leavingRef = useRef(false);
  const doneRef = useRef(false);
  const readyRef = useRef(ready);
  readyRef.current = ready;

  const finish = () => {
    if (doneRef.current) return;
    doneRef.current = true;
    onDone();
  };

  const beginLeave = () => {
    if (leavingRef.current) return;
    leavingRef.current = true;
    setLeaving(true);
    window.setTimeout(finish, FADE_MS + 160); // fallback if transitionend doesn't fire
  };

  useEffect(() => {
    splashRef.current?.focus();
  }, []);

  // ---- canvas FX (orbiters + particle plexus + scan beam), ported from app.js
  useEffect(() => {
    const canvas = canvasRef.current;
    const ctx = canvas?.getContext("2d");
    if (!canvas || !ctx) return;

    const BLUE: RGB = [12, 101, 255];
    const CYAN: RGB = [18, 203, 255];
    const GREEN: RGB = [49, 223, 40];
    const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
    const mix = (a: RGB, b: RGB, t: number): RGB => [
      Math.round(lerp(a[0], b[0], t)),
      Math.round(lerp(a[1], b[1], t)),
      Math.round(lerp(a[2], b[2], t)),
    ];
    const rgba = (c: RGB, a: number) => `rgba(${c[0]}, ${c[1]}, ${c[2]}, ${a})`;

    let w = 0;
    let h = 0;
    let orbiters: Orbiter[] = [];
    let points: Particle[] = [];
    let raf = 0;
    const start = performance.now();
    let finishStart = 0; // timestamp when we began the CAP → 100% finish
    let finishFrom = 0; // progress value at the moment the finish started
    let completed = false;
    let lastUiUpdate = 0;

    const updateProgress = (now: number) => {
      let progress: number;
      if (finishStart === 0) {
        progress = Math.min(CAP, easeOutCubic((now - start) / RAMP_MS));
        if (readyRef.current && now - start >= MIN_MS) {
          finishStart = now;
          finishFrom = progress;
        }
      } else {
        const t = Math.min(1, (now - finishStart) / FINISH_MS);
        progress = finishFrom + (1 - finishFrom) * easeOutCubic(t);
        if (t >= 1 && !completed) {
          completed = true;
          window.setTimeout(beginLeave, HOLD_MS);
        }
      }
      // The HUD does not need frame-rate React updates. Canvas motion remains
      // imperative while accessible text/progress updates at a calm cadence.
      if (now - lastUiUpdate >= 100 || progress >= 1) {
        lastUiUpdate = now;
        setProg(progress);
      }
      return progress;
    };

    const buildFx = () => {
      const scale = Math.min(w, h);
      orbiters = [];
      for (let i = 0; i < 16; i++) {
        orbiters.push({
          rx: scale * (0.1 + i * 0.009),
          ry: scale * (0.06 + i * 0.007),
          speed: 0.00035 + i * 0.00003,
          angle: Math.random() * Math.PI * 2,
          color: i % 2 === 0 ? mix(BLUE, CYAN, Math.random()) : mix(CYAN, GREEN, Math.random()),
          size: 2 + Math.random() * 3,
        });
      }
      points = Array.from({ length: 84 }, (_, i): Particle => {
        const ring = i % 3;
        return {
          a: Math.random() * Math.PI * 2,
          r: scale * (0.06 + Math.random() * 0.14 + ring * 0.014),
          wobble: 0.9 + Math.random() * 2.5,
          phase: Math.random() * Math.PI * 2,
          color: Math.random() < 0.5 ? mix(BLUE, CYAN, Math.random()) : mix(CYAN, GREEN, Math.random()),
        };
      });
    };

    const resize = () => {
      const dpr = Math.max(1, window.devicePixelRatio || 1);
      w = window.innerWidth;
      h = window.innerHeight;
      canvas.width = Math.floor(w * dpr);
      canvas.height = Math.floor(h * dpr);
      canvas.style.width = w + "px";
      canvas.style.height = h + "px";
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      buildFx();
    };

    const draw = (now: number) => {
      // Ramp to CAP, hold until the app is ready, then ease to 100%.
      const progress = updateProgress(now);

      ctx.clearRect(0, 0, w, h);
      ctx.save();
      ctx.globalCompositeOperation = "screen";
      const cx = w * 0.5;
      const cy = h * 0.43;
      const scale = Math.min(w, h);

      const g = ctx.createRadialGradient(cx, cy, 10, cx, cy, scale * 0.28);
      g.addColorStop(0, "rgba(18,203,255,0.12)");
      g.addColorStop(0.55, "rgba(49,223,40,0.05)");
      g.addColorStop(1, "rgba(0,0,0,0)");
      ctx.fillStyle = g;
      ctx.beginPath();
      ctx.arc(cx, cy, scale * 0.28, 0, Math.PI * 2);
      ctx.fill();

      orbiters.forEach((o, idx) => {
        const ang = o.angle + now * o.speed;
        const x = cx + Math.cos(ang) * o.rx;
        const y = cy + Math.sin(ang) * o.ry * 0.88;
        ctx.strokeStyle = rgba(o.color, 0.1);
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.ellipse(cx, cy, o.rx, o.ry * 0.88, (idx % 3) * 0.55, 0, Math.PI * 2);
        ctx.stroke();
        ctx.shadowBlur = 12;
        ctx.shadowColor = rgba(o.color, 0.9);
        ctx.fillStyle = rgba(o.color, 0.82);
        ctx.beginPath();
        ctx.arc(x, y, o.size, 0, Math.PI * 2);
        ctx.fill();
      });

      const active = Math.floor(points.length * progress);
      const live: { x: number; y: number; color: RGB }[] = [];
      for (let i = 0; i < active; i++) {
        const p = points[i];
        const ang = p.a + Math.sin(now * 0.0008 + p.phase) * 0.4;
        const rad = p.r + Math.sin(now * 0.0017 + p.phase) * p.wobble * 4;
        live.push({ x: cx + Math.cos(ang) * rad, y: cy + Math.sin(ang) * rad * 0.84, color: p.color });
      }
      for (let i = 0; i < live.length; i++) {
        const a = live[i];
        for (let j = i + 1; j < live.length; j++) {
          const b = live[j];
          const dx = a.x - b.x;
          const dy = a.y - b.y;
          const d2 = dx * dx + dy * dy;
          if (d2 < 9000) {
            ctx.strokeStyle = rgba(mix(a.color, b.color, 0.5), 0.08 * (1 - d2 / 9000));
            ctx.lineWidth = 1;
            ctx.beginPath();
            ctx.moveTo(a.x, a.y);
            ctx.lineTo(b.x, b.y);
            ctx.stroke();
          }
        }
      }
      ctx.shadowBlur = 12;
      live.forEach((p) => {
        ctx.shadowColor = rgba(p.color, 0.8);
        ctx.fillStyle = rgba(p.color, 0.65);
        ctx.beginPath();
        ctx.arc(p.x, p.y, 2, 0, Math.PI * 2);
        ctx.fill();
      });

      const scanY = cy - scale * 0.13 + ((now * 0.06) % (scale * 0.25));
      const beam = ctx.createLinearGradient(cx - scale * 0.2, scanY, cx + scale * 0.2, scanY);
      beam.addColorStop(0, "rgba(12,101,255,0)");
      beam.addColorStop(0.35, "rgba(18,203,255,0.18)");
      beam.addColorStop(0.5, "rgba(255,255,255,0.48)");
      beam.addColorStop(0.65, "rgba(49,223,40,0.18)");
      beam.addColorStop(1, "rgba(49,223,40,0)");
      ctx.strokeStyle = beam;
      ctx.lineWidth = 1.6;
      ctx.beginPath();
      ctx.moveTo(cx - scale * 0.22, scanY);
      ctx.lineTo(cx + scale * 0.22, scanY);
      ctx.stroke();
      ctx.restore();

      if (!leavingRef.current) raf = requestAnimationFrame(draw);
    };

    resize();
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      // The background image is the static reduced-motion frame. Keep startup
      // progress working without running the canvas animation loop.
      const interval = window.setInterval(() => updateProgress(performance.now()), 100);
      updateProgress(performance.now());
      return () => window.clearInterval(interval);
    }
    window.addEventListener("resize", resize);
    raf = requestAnimationFrame(draw);
    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("resize", resize);
    };
  }, []);

  // ---- HUD values derived from progress (ported from updateUi) ----
  const pct = Math.max(1, Math.round(prog * 100));
  const engine = Math.round(12 + prog * 88);
  const model = Math.round(6 + prog * 94);
  const adaptive = Math.round(10 + prog * 82);
  const predictive = Math.round(5 + prog * 86);
  const logCount = Math.max(3, Math.min(LOGS.length, Math.floor(prog * LOGS.length) + 1));
  const metrics: [string, number][] = [
    ["Cognitive Engine", engine],
    ["Learning Model", model],
    ["Adaptive System", adaptive],
    ["Predictive Layer", predictive],
  ];

  return (
    <div
      ref={splashRef}
      className={`novera-loader${leaving ? " leaving" : ""}`}
      role="dialog"
      aria-modal="true"
      aria-label="Initializing Novera"
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.key === "Tab") event.preventDefault();
      }}
      onTransitionEnd={(e) => {
        if (e.propertyName === "opacity" && leavingRef.current) finish();
      }}
    >
      <div className="bg" />
      <canvas className="fx" ref={canvasRef} aria-hidden="true" />
      <div className="global-scan" aria-hidden="true" />

      <section className="hud">
        <div className="corner tl" />
        <div className="corner tr" />
        <div className="corner bl" />
        <div className="corner br" />

        <div className="brand-top">
          <img src="/novera-loader-logo.png" alt="Novera logo" className="logo" />
          <div>
            <div className="brand-small">Novera</div>
            <div className="brand-sub">Cognitive Interface</div>
          </div>
        </div>

        <div className="title-block">
          <h1>Novera</h1>
          <div className="subtitle">Initializing intelligence core</div>
          <div className="micro-progress">
            <span style={{ width: `${pct}%` }} />
          </div>
        </div>

        <aside className="right-panel">
          <div className="panel-title">AI Core Status</div>
          {metrics.map(([name, value]) => (
            <div className="metric" key={name}>
              <span className="name">{name}</span>
              <span className="value">{value}%</span>
              <div className="line">
                <span style={{ width: `${value}%` }} />
              </div>
            </div>
          ))}

          <div className="log-title">System Log</div>
          <div className="log">
            {LOGS.slice(0, logCount).map((line) => (
              <div key={line}>{line}</div>
            ))}
          </div>
        </aside>

        <div
          className="bottom-loader"
          role="progressbar"
          aria-label="Startup progress"
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div className="percent">{pct}%</div>
          <div className="state">{stateFor(prog)}</div>
          <div className="progress-bar">
            <div className="progress-fill" style={{ width: `${pct}%` }} />
          </div>
        </div>
      </section>
    </div>
  );
}
