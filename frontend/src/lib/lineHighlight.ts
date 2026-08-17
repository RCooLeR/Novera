// Dependency-free, per-line syntax highlighting for the big-file viewer.
//
// Ported from Quarry's editor highlighters (highlight.ts / sqlHighlight.ts),
// minus the CodeMirror decoration/theme wrapper. Everything works PER LINE so a
// collapsed/truncated long line never bleeds a string/comment colour into the
// next, and tokenising stays bounded to one window's worth of lines. Each
// tokenizer turns a line into colored segments the viewer renders as spans.

import { languageForPath } from "./lang";

export type Seg = { t: string; c: string }; // text, css class ("" = plain)
export type LineTokenizer = (text: string) => Seg[];

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const kw = (s: string) => new Set(s.split(/\s+/).filter(Boolean));

// Walk regex matches, emitting plain text between classified tokens. classify
// returns a css class for a match, or null to leave it as plain text.
function tokenizeWith(re: RegExp, classify: (m: RegExpExecArray, text: string) => string | null): LineTokenizer {
  return (text: string): Seg[] => {
    if (!text) return [{ t: "", c: "" }];
    const segs: Seg[] = [];
    let last = 0;
    re.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text)) !== null) {
      if (m[0].length === 0) {
        re.lastIndex++;
        continue;
      }
      const cls = classify(m, text);
      if (cls) {
        if (m.index > last) segs.push({ t: text.slice(last, m.index), c: "" });
        segs.push({ t: m[0], c: cls });
        last = m.index + m[0].length;
      }
    }
    if (last < text.length) segs.push({ t: text.slice(last), c: "" });
    return segs.length ? segs : [{ t: text, c: "" }];
  };
}

// ---- SQL ------------------------------------------------------------------

const SQL_KEYWORDS = new Set(
  (
    "ADD ALTER AND AS ASC AUTO_INCREMENT BEGIN BETWEEN BIGINT BINARY BLOB BOOLEAN BY " +
    "CASCADE CASE CHANGE CHAR CHARACTER CHARSET COLLATE COLUMN COMMIT CONSTRAINT CREATE " +
    "CROSS CURRENT_TIMESTAMP DATABASE DATABASES DATE DATETIME DECIMAL DEFAULT DEFINER " +
    "DELETE DESC DISTINCT DOUBLE DROP ELSE ENGINE ENUM EXISTS FALSE FLOAT FOREIGN FROM " +
    "FULL FULLTEXT FUNCTION GRANT GROUP HAVING IF IGNORE IN INDEX INNER INSERT INT INTEGER " +
    "INTO IS JOIN KEY KEYS LEFT LIKE LIMIT LOCK LONGBLOB LONGTEXT MEDIUMBLOB MEDIUMINT " +
    "MEDIUMTEXT MODIFY NOT NULL ON OR ORDER OUTER PRIMARY PROCEDURE REFERENCES RENAME " +
    "REPLACE RIGHT SELECT SET SMALLINT START TABLE TABLES TEMPORARY TEXT THEN TIME TIMESTAMP " +
    "TINYINT TINYTEXT TO TRANSACTION TRIGGER TRUE TRUNCATE UNION UNIQUE UNLOCK UNSIGNED " +
    "UPDATE USE USING VALUES VARBINARY VARCHAR VIEW WHEN WHERE WITH ZEROFILL"
  ).split(" "),
);

// 1 comment  2 'string'  3 "string"  4 `ident`  5 number  6 word
// [^!-\uFFFF] means an ASCII control/space byte (U+0000 through U+0020)
// without embedding a control-character escape that eslint rejects.
const SQL_TOKEN =
  /(--(?=$|[^!-\uFFFF])[^\r\n]*|#[^\r\n]*|\/\*[\s\S]*?\*\/|\/\*[^\r\n]*)|('(?:\\.|''|[^'\\])*'?)|("(?:\\.|""|[^"\\])*"?)|(`(?:``|[^`])*`?)|(\b\d[\d.]*\b)|([A-Za-z_][A-Za-z0-9_$]*)/g;

const sqlTokenizer = tokenizeWith(SQL_TOKEN, (m) => {
  if (m[1] !== undefined) return "hl-comment";
  if (m[2] !== undefined || m[3] !== undefined) return "hl-string";
  if (m[4] !== undefined) return "hl-ident";
  if (m[5] !== undefined) return "hl-number";
  if (m[6] !== undefined && SQL_KEYWORDS.has(m[6].toUpperCase())) return "hl-keyword";
  return null;
});

// ---- generic languages ----------------------------------------------------

type LineKeys = "json" | "yaml" | "none";
interface LangProfile {
  lineComment: string[];
  block?: [string, string];
  strings: string[];
  keywords: Set<string>;
  keys: LineKeys;
}

const KW = {
  js: kw("abstract any as async await boolean break case catch class const continue debugger declare default delete do else enum export extends false finally for from function get if implements import in instanceof interface let new null of private protected public readonly return set static super switch this throw true try type typeof undefined var void while yield"),
  py: kw("and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield self"),
  go: kw("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false iota string int int64 byte rune bool error"),
  rust: kw("as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while"),
  java: kw("abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static super switch synchronized this throw throws transient true false null try void volatile while var record"),
  c: kw("auto break case char const continue default do double else enum extern float for goto if inline int long register restrict return short signed sizeof static struct switch typedef union unsigned void volatile while bool true false NULL include define ifdef ifndef endif pragma"),
  cpp: kw("alignas alignof auto bool break case catch char class const constexpr continue decltype default delete do double else enum explicit export extern false float for friend goto if inline int long mutable namespace new noexcept nullptr operator private protected public return short signed sizeof static struct switch template this throw true try typedef typename union unsigned using virtual void volatile while"),
  cs: kw("abstract as base bool break byte case catch char checked class const continue decimal default delegate do double else enum event explicit extern false finally fixed float for foreach goto if implicit in int interface internal is lock long namespace new null object operator out override params private protected public readonly ref return sbyte sealed short sizeof static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using var virtual void volatile while async await yield"),
  php: kw("abstract and array as break callable case catch class clone const continue declare default do echo else elseif empty enddeclare endfor endforeach endif endswitch endwhile extends final finally fn for foreach function global goto if implements include include_once instanceof insteadof interface isset list namespace new or print private protected public require require_once return static switch throw trait try unset use var while xor yield true false null"),
  ruby: kw("alias and begin break case class def defined do else elsif end ensure false for if in module next nil not or redo rescue retry return self super then true unless until when while yield attr_accessor attr_reader attr_writer require require_relative puts"),
  shell: kw("if then else elif fi case esac for while until do done function in select time return break continue echo export local readonly declare set unset source alias"),
  kotlin: kw("abstract as break by catch class companion const continue crossinline data do dynamic else enum external false final finally for fun get if import in infix init inline inner interface internal is lateinit object open operator out override package private protected public reified return sealed set super suspend this throw true try typealias typeof val var vararg when where while null"),
  swift: kw("associatedtype class deinit enum extension fileprivate func import init inout internal let open operator private protocol public rethrows static struct subscript typealias var break case continue default defer do else fallthrough for guard if in repeat return switch where while as catch false is nil throw throws true try"),
  json: kw("true false null"),
  yaml: kw("true false null yes no on off"),
  toml: kw("true false"),
  css: kw("important inherit initial unset none auto"),
};

const EMPTY = new Set<string>();
const cfam = (keywords: Set<string>): LangProfile => ({
  lineComment: ["//"],
  block: ["/*", "*/"],
  strings: ['"', "'", "`"],
  keywords,
  keys: "none",
});
const hashfam = (keywords: Set<string>, keys: LineKeys = "none"): LangProfile => ({
  lineComment: ["#"],
  strings: ['"', "'"],
  keywords,
  keys,
});

// One profile per Monaco language id (the ids produced by languageForPath in
// lang.ts), so the big-file viewer highlights EVERY text type Novera
// recognises — comments/strings/numbers universally, plus keywords where we
// have a set. SQL and CSV are handled separately in highlighterFor.
const LANG_PROFILE: Record<string, LangProfile> = {
  go: cfam(KW.go),
  javascript: cfam(KW.js),
  typescript: cfam(KW.js),
  java: cfam(KW.java),
  groovy: cfam(EMPTY),
  c: cfam(KW.c),
  cpp: cfam(KW.cpp),
  csharp: cfam(KW.cs),
  rust: cfam(KW.rust),
  kotlin: cfam(KW.kotlin),
  swift: cfam(KW.swift),
  scala: cfam(EMPTY),
  dart: cfam(EMPTY),
  proto: cfam(EMPTY),
  php: { lineComment: ["//", "#"], block: ["/*", "*/"], strings: ['"', "'"], keywords: KW.php, keys: "none" },
  python: hashfam(KW.py),
  ruby: hashfam(KW.ruby),
  perl: hashfam(EMPTY),
  r: hashfam(EMPTY),
  elixir: hashfam(EMPTY),
  julia: hashfam(EMPTY),
  shell: hashfam(KW.shell),
  powershell: { lineComment: ["#"], block: ["<#", "#>"], strings: ['"', "'"], keywords: EMPTY, keys: "none" },
  dockerfile: hashfam(
    kw("FROM RUN CMD LABEL MAINTAINER EXPOSE ENV ADD COPY ENTRYPOINT VOLUME USER WORKDIR ARG ONBUILD STOPSIGNAL HEALTHCHECK SHELL AS"),
  ),
  makefile: hashfam(EMPTY),
  yaml: hashfam(KW.yaml, "yaml"),
  ini: { lineComment: [";", "#"], strings: ['"', "'"], keywords: EMPTY, keys: "yaml" },
  hcl: { lineComment: ["#", "//"], block: ["/*", "*/"], strings: ['"'], keywords: kw("true false null"), keys: "yaml" },
  graphql: {
    lineComment: ["#"],
    strings: ['"'],
    keywords: kw("query mutation subscription type input enum interface union scalar schema fragment on implements extend directive true false null"),
    keys: "none",
  },
  json: { lineComment: [], strings: ['"'], keywords: KW.json, keys: "json" },
  lua: {
    lineComment: ["--"],
    block: ["--[[", "]]"],
    strings: ['"', "'"],
    keywords: kw("and break do else elseif end false for function goto if in local nil not or repeat return then true until while"),
    keys: "none",
  },
  haskell: {
    lineComment: ["--"],
    block: ["{-", "-}"],
    strings: ['"'],
    keywords: kw("module import where let in do case of class instance data type newtype deriving if then else"),
    keys: "none",
  },
  clojure: { lineComment: [";"], strings: ['"'], keywords: EMPTY, keys: "none" },
  erlang: { lineComment: ["%"], strings: ['"'], keywords: EMPTY, keys: "none" },
  fsharp: {
    lineComment: ["//"],
    block: ["(*", "*)"],
    strings: ['"'],
    keywords: kw("let mutable rec in if then else match with for while do done begin end fun function module namespace open type member static abstract interface inherit new true false null"),
    keys: "none",
  },
  html: { lineComment: [], block: ["<!--", "-->"], strings: ['"', "'"], keywords: EMPTY, keys: "none" },
  xml: { lineComment: [], block: ["<!--", "-->"], strings: ['"', "'"], keywords: EMPTY, keys: "none" },
  vue: { lineComment: [], block: ["<!--", "-->"], strings: ['"', "'"], keywords: EMPTY, keys: "none" },
  css: { lineComment: [], block: ["/*", "*/"], strings: ['"', "'"], keywords: KW.css, keys: "none" },
  scss: { lineComment: ["//"], block: ["/*", "*/"], strings: ['"', "'"], keywords: KW.css, keys: "none" },
  less: { lineComment: ["//"], block: ["/*", "*/"], strings: ['"', "'"], keywords: KW.css, keys: "none" },
  bat: { lineComment: ["::"], strings: ['"'], keywords: EMPTY, keys: "none" },
};

const NUMBER = "\\b0[xX][0-9a-fA-F]+\\b|\\b\\d[\\d_]*(?:\\.\\d+)?(?:[eE][+-]?\\d+)?\\b";
const WORD = "[A-Za-z_$][A-Za-z0-9_$]*";
const NEVER = "((?!))";

function buildRegex(p: LangProfile): RegExp {
  const comments: string[] = [];
  if (p.block) comments.push(esc(p.block[0]) + "[\\s\\S]*?" + esc(p.block[1]), esc(p.block[0]) + "[^\\n]*");
  for (const lc of p.lineComment) comments.push(esc(lc) + "[^\\n]*");
  const strings: string[] = [];
  for (const q of p.strings) {
    const e = esc(q);
    strings.push(e + "(?:\\\\.|" + e + e + "|[^" + e + "\\\\])*" + e + "?");
  }
  const g = (arr: string[]) => (arr.length ? "(" + arr.join("|") + ")" : NEVER);
  return new RegExp([g(comments), g(strings), "(" + NUMBER + ")", "(" + WORD + ")"].join("|"), "g");
}

function genericTokenizer(profile: LangProfile): LineTokenizer {
  const re = buildRegex(profile);
  return tokenizeWith(re, (m, text) => {
    if (m[1] !== undefined) return "hl-comment";
    if (m[2] !== undefined)
      return profile.keys !== "none" && /^\s*:/.test(text.slice(m.index + m[0].length)) ? "hl-key" : "hl-string";
    if (m[3] !== undefined) return "hl-number";
    if (m[4] !== undefined) {
      if (profile.keywords.has(m[4]) || profile.keywords.has(m[4].toLowerCase())) return "hl-keyword";
      if (profile.keys !== "none" && /^\s*:/.test(text.slice(m.index + m[0].length))) return "hl-key";
    }
    return null;
  });
}

// ---- rainbow CSV ----------------------------------------------------------

function detectDelimiter(text: string): string {
  let best = ",";
  let bestN = -1;
  for (const d of [",", "\t", ";", "|"]) {
    const n = text.split(d).length;
    if (n > bestN) {
      bestN = n;
      best = d;
    }
  }
  return best;
}

function csvFieldRanges(text: string, delim: string): Array<[number, number]> {
  const ranges: Array<[number, number]> = [];
  let start = 0;
  let inQuotes = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === '"') {
      if (inQuotes && text[i + 1] === '"') {
        i++;
        continue;
      }
      inQuotes = !inQuotes;
    } else if (ch === delim && !inQuotes) {
      ranges.push([start, i]);
      start = i + 1;
    }
  }
  ranges.push([start, text.length]);
  return ranges;
}

function csvRainbow(): LineTokenizer {
  let delim = "";
  return (text: string): Seg[] => {
    if (!text) return [{ t: "", c: "" }];
    if (!delim) delim = detectDelimiter(text);
    const segs: Seg[] = [];
    let last = 0;
    let col = 0;
    for (const [s, e] of csvFieldRanges(text, delim)) {
      if (s > last) segs.push({ t: text.slice(last, s), c: "" }); // delimiter
      if (e > s) segs.push({ t: text.slice(s, e), c: "hl-csv-" + (col % 8) });
      last = e;
      col++;
    }
    if (last < text.length) segs.push({ t: text.slice(last), c: "" });
    return segs.length ? segs : [{ t: text, c: "" }];
  };
}

// ---- dispatch -------------------------------------------------------------

function extOf(path: string): string {
  const base = path.replace(/[\\/]+$/, "");
  const dot = base.lastIndexOf(".");
  const slash = Math.max(base.lastIndexOf("/"), base.lastIndexOf("\\"));
  return dot > slash ? base.slice(dot + 1).toLowerCase() : "";
}

/** Pick a per-line tokenizer for any file Novera recognises (by Monaco language
 *  id) plus SQL/CSV special-cases. Returns null for plain text / unknown. */
export function highlighterFor(detected: string, path: string): LineTokenizer | null {
  const d = (detected || "").toLowerCase();
  const ext = extOf(path);
  if (d === "csv" || d === "tsv" || ext === "csv" || ext === "tsv") return csvRainbow();
  const lang = languageForPath(path);
  if (lang === "sql" || d === "sql" || ext === "dump") return sqlTokenizer;
  const p = LANG_PROFILE[lang];
  return p ? genericTokenizer(p) : null;
}
