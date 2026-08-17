package reshape

import (
	"fmt"

	sqldelimiter "novera/internal/bigfile/plugins/sql/delimiter"
)

// reshapeSafetyGuard is a deliberately small, fixed-state SQL safety lexer.
// It does not try to parse every dialect. Instead it rejects constructs whose
// internal semicolons or INSERT-looking payload bytes could be mistaken for
// top-level statements by the conservative reshape grammar.
//
// State and token storage are constant-sized, so the guard remains independent
// of source-file, statement, comment, and identifier length.
type reshapeSafetyGuard struct {
	offset int64

	mode       reshapeGuardMode
	escaped    bool
	justExited byte
	prevQ      bool
	prevLess   bool

	pendingDash  bool
	dashComment  bool
	pendingSlash bool

	blockPrevStar  bool
	blockPrevSlash bool

	lineHasContent      bool
	statementHasContent bool

	bomState uint8 // 0: undecided; 1: EF; 2: EF BB; 3: resolved

	delimiter sqldelimiter.Detector
	client    reshapeClientCommandDetector
	compound  reshapeCompoundPrefixDetector
	dollar    reshapeDollarQuoteDetector
}

type reshapeGuardMode uint8

const (
	reshapeGuardNormal reshapeGuardMode = iota
	reshapeGuardSingleQuote
	reshapeGuardDoubleQuote
	reshapeGuardBacktick
	reshapeGuardLineComment
	reshapeGuardBlockComment
)

func (g *reshapeSafetyGuard) Step(c byte) error {
	offset := g.offset
	g.offset++

	// Keep the existing raw client-directive refusal first so DELIMITER $$ is
	// classified as a delimiter error rather than as a dollar-quote hazard.
	if g.delimiter.Step(c) {
		return fmt.Errorf("%w at byte %d", ErrUnsupportedDelimiter, offset)
	}
	if g.consumeLeadingBOM(c, offset) {
		return nil
	}
	switch g.client.Step(c, g.mode == reshapeGuardNormal, g.statementHasContent, g.compound.Irrelevant()) {
	case reshapeClientBatchCommand:
		return fmt.Errorf("%w at byte %d: client-side batch or meta-command", ErrUnsupportedCompoundStatement, offset)
	case reshapeClientDelimiterCommand:
		return fmt.Errorf("%w at byte %d: client-side terminator command", ErrUnsupportedDelimiter, offset)
	}

	switch g.mode {
	case reshapeGuardSingleQuote:
		g.noteQuotedByte(c)
		switch {
		case g.escaped:
			g.escaped = false
		case c == '\\':
			g.escaped = true
		case c == '\'':
			g.mode = reshapeGuardNormal
			g.justExited = c
		}
		return nil
	case reshapeGuardDoubleQuote:
		g.noteQuotedByte(c)
		switch {
		case g.escaped:
			g.escaped = false
		case c == '\\':
			g.escaped = true
		case c == '"':
			g.mode = reshapeGuardNormal
			g.justExited = c
		}
		return nil
	case reshapeGuardBacktick:
		g.noteQuotedByte(c)
		if c == '`' {
			g.mode = reshapeGuardNormal
			g.justExited = c
		}
		return nil
	case reshapeGuardLineComment:
		if c == '\n' || c == '\r' {
			g.mode = reshapeGuardNormal
			g.lineHasContent = false
		}
		return nil
	case reshapeGuardBlockComment:
		if c == '\n' || c == '\r' {
			g.lineHasContent = false
		}
		if c == '/' && g.blockPrevStar {
			g.mode = reshapeGuardNormal
			g.blockPrevStar = false
			g.blockPrevSlash = false
			if !g.lineHasContent {
				// Block comments are SQL trivia. The client detector saw the raw
				// opener before the lexer could prove it was a comment, so restore
				// first-live-token detection for bytes following leading trivia.
				g.client.ResumeAfterLeadingTrivia()
			}
			return nil
		}
		if c == '*' && g.blockPrevSlash {
			return fmt.Errorf("%w at byte %d: nested block comment", ErrUnsupportedLexicalConstruct, offset-1)
		}
		g.blockPrevStar = c == '*'
		g.blockPrevSlash = c == '/'
		return nil
	}

	if g.dashComment {
		g.dashComment = false
		if !isSpace(c) {
			return fmt.Errorf("%w at byte %d: '--' comment lacks separating whitespace", ErrUnsupportedLexicalConstruct, offset-2)
		}
		if c == '\n' || c == '\r' {
			g.lineHasContent = false
			return nil
		}
		g.mode = reshapeGuardLineComment
		return nil
	}
	if g.pendingDash {
		g.pendingDash = false
		if c == '-' {
			g.dashComment = true
			return nil
		}
		g.markLive('-')
	}
	if g.pendingSlash {
		g.pendingSlash = false
		if c == '*' {
			// The current '*' belongs only to the opener. In particular, the
			// overlapping sequence "/*/" is not a closed comment.
			g.mode = reshapeGuardBlockComment
			g.blockPrevStar = false
			g.blockPrevSlash = false
			return nil
		}
		g.markLive('/')
	}

	if g.justExited != 0 {
		quote := g.justExited
		g.justExited = 0
		if c == quote {
			switch quote {
			case '\'':
				g.mode = reshapeGuardSingleQuote
			case '"':
				g.mode = reshapeGuardDoubleQuote
			default:
				g.mode = reshapeGuardBacktick
			}
			return nil
		}
	}

	return g.stepNormal(c, offset)
}

func (g *reshapeSafetyGuard) stepNormal(c byte, offset int64) error {
	if g.prevLess && c == '<' {
		return fmt.Errorf("%w at byte %d: Oracle block label", ErrUnsupportedLexicalConstruct, offset-1)
	}
	g.prevLess = false
	if c == '\'' && g.prevQ {
		return fmt.Errorf("%w at byte %d: Oracle q-quoted literal", ErrUnsupportedLexicalConstruct, offset-1)
	}
	g.prevQ = false

	if g.dollar.Step(c) {
		return fmt.Errorf("%w at byte %d: PostgreSQL dollar-quoted body", ErrUnsupportedCompoundStatement, offset)
	}
	if g.compound.Step(c) {
		return fmt.Errorf("%w at byte %d", ErrUnsupportedCompoundStatement, offset)
	}

	switch c {
	case '\'':
		g.markLive(c)
		g.mode = reshapeGuardSingleQuote
		g.escaped = false
	case '"':
		g.markLive(c)
		g.mode = reshapeGuardDoubleQuote
		g.escaped = false
	case '`':
		g.markLive(c)
		g.mode = reshapeGuardBacktick
	case '[':
		return fmt.Errorf("%w at byte %d: bracket-quoted construct", ErrUnsupportedLexicalConstruct, offset)
	case '#':
		if g.lineHasContent || g.statementHasContent {
			return fmt.Errorf("%w at byte %d: ambiguous inline '#' comment or operator", ErrUnsupportedLexicalConstruct, offset)
		}
		g.mode = reshapeGuardLineComment
	case '-':
		g.pendingDash = true
	case '/':
		g.pendingSlash = true
	case ';':
		g.lineHasContent = true
		g.statementHasContent = false
		g.compound.Reset()
		g.dollar.Reset()
	case '<':
		g.markLive(c)
		g.prevLess = true
	default:
		g.markLive(c)
		g.prevQ = c == 'q' || c == 'Q'
	}
	return nil
}

func (g *reshapeSafetyGuard) Finish() error {
	if g.delimiter.Finish() {
		return ErrUnsupportedDelimiter
	}
	if g.dashComment {
		// A comment ending immediately after "--" is unambiguous at EOF.
		g.dashComment = false
	}
	switch g.client.Finish() {
	case reshapeClientBatchCommand:
		return ErrUnsupportedCompoundStatement
	case reshapeClientDelimiterCommand:
		return ErrUnsupportedDelimiter
	}
	if g.compound.Finish() {
		return ErrUnsupportedCompoundStatement
	}
	switch g.mode {
	case reshapeGuardSingleQuote, reshapeGuardDoubleQuote, reshapeGuardBacktick:
		return fmt.Errorf("%w: unterminated quoted literal or identifier", ErrUnsupportedLexicalConstruct)
	case reshapeGuardBlockComment:
		return fmt.Errorf("%w: unterminated block comment", ErrUnsupportedLexicalConstruct)
	}
	if g.bomState == 1 || g.bomState == 2 {
		return fmt.Errorf("%w: incomplete UTF-8 byte-order mark", ErrUnsupportedLexicalConstruct)
	}
	return nil
}

func (g *reshapeSafetyGuard) markLive(c byte) {
	if c == '\n' || c == '\r' {
		g.lineHasContent = false
		return
	}
	if isSpace(c) {
		return
	}
	g.lineHasContent = true
	g.statementHasContent = true
}

func (g *reshapeSafetyGuard) noteQuotedByte(c byte) {
	if c == '\n' || c == '\r' {
		g.lineHasContent = false
	}
}

func (g *reshapeSafetyGuard) consumeLeadingBOM(c byte, offset int64) bool {
	if g.bomState == 3 || offset > 2 || g.lineHasContent || g.statementHasContent {
		g.bomState = 3
		return false
	}
	switch g.bomState {
	case 0:
		if offset == 0 && c == 0xef {
			g.bomState = 1
			return true
		}
	case 1:
		if offset == 1 && c == 0xbb {
			g.bomState = 2
			return true
		}
	case 2:
		if offset == 2 && c == 0xbf {
			g.bomState = 3
			return true
		}
	}
	// A malformed prefix is live input. The earlier high bytes cannot form an
	// ASCII hazard token, but they must prevent a later '#' from being promoted
	// to a line-leading between-statements comment.
	g.bomState = 3
	if offset > 0 {
		g.lineHasContent = true
		g.statementHasContent = true
	}
	return false
}

type reshapeCompoundPrefixDetector struct {
	stage    uint8 // 0:first; 1:DDL object; 2:after BEGIN; 3:irrelevant; 4:CREATE TYPE; 5:SET; 6:EXECUTE
	token    [16]byte
	tokenLen uint8
	overflow bool
	create   bool
}

func (d *reshapeCompoundPrefixDetector) Step(c byte) bool {
	if d.stage == 3 {
		return false
	}
	if reshapeASCIIWord(c) {
		if int(d.tokenLen) < len(d.token) {
			d.token[d.tokenLen] = lower(c)
			d.tokenLen++
		} else {
			d.overflow = true
		}
		return false
	}
	return d.finishToken()
}

func (d *reshapeCompoundPrefixDetector) finishToken() bool {
	if d.tokenLen == 0 && !d.overflow {
		return false
	}
	token := ""
	if !d.overflow {
		token = string(d.token[:d.tokenLen])
	}
	d.tokenLen = 0
	d.overflow = false
	switch d.stage {
	case 0:
		switch token {
		case "create", "recreate":
			d.create = true
			d.stage = 1
		case "alter", "replace":
			d.stage = 1
		case "begin":
			d.stage = 2
		case "set":
			d.stage = 5
		case "execute":
			d.stage = 6
		case "declare", "copy", "do", "if", "loop", "while", "for", "repeat":
			return true
		default:
			d.stage = 3
		}
		return false
	case 2:
		switch token {
		case "transaction", "work", "deferred", "immediate", "exclusive":
			d.stage = 3
			return false
		default:
			return true
		}
	case 4:
		if token == "body" {
			return true
		}
		d.stage = 3
		return false
	case 5:
		if token == "term" {
			return true
		}
		d.stage = 3
		return false
	case 6:
		if token == "block" {
			return true
		}
		d.stage = 3
		return false
	}

	switch token {
	case "definer":
		// MySQL account names may legally be unquoted. An account such as
		// user@localhost would otherwise hit the ordinary CREATE USER stop
		// token before a later PROCEDURE/TRIGGER/FUNCTION/EVENT token, hiding
		// the compound body. A DEFINER-bearing DDL prefix is therefore unsafe
		// for this deliberately small lexer regardless of its eventual object.
		return true
	case "trigger", "procedure", "proc", "function", "event", "rule", "package", "module", "macro", "begin":
		// Some dialects attach a procedural BEGIN/END block to DDL object
		// kinds outside this detector's deliberately small allowlist (for
		// example Snowflake CREATE TASK). Reject before the first internal
		// semicolon can reset statement ownership.
		return true
	case "type":
		if d.create {
			d.stage = 4
		} else {
			d.stage = 3
		}
	case "into", "table", "view", "index", "database", "schema", "sequence",
		"user", "role", "server", "tablespace", "extension", "collation", "domain",
		"operator", "aggregate", "policy", "publication", "subscription":
		d.stage = 3
	}
	return false
}

func (d *reshapeCompoundPrefixDetector) Finish() bool { return d.finishToken() }

func (d *reshapeCompoundPrefixDetector) Reset() { *d = reshapeCompoundPrefixDetector{} }

func (d *reshapeCompoundPrefixDetector) Irrelevant() bool { return d.stage == 3 }

type reshapeDollarQuoteDetector struct {
	state     uint8 // 0:idle; 1:saw boundary '$'; 2:valid tag body
	prevIdent bool
}

func (d *reshapeDollarQuoteDetector) Step(c byte) bool {
	switch d.state {
	case 0:
		if c == '$' && !d.prevIdent {
			d.state = 1
			d.prevIdent = false
		} else {
			d.prevIdent = reshapeDollarIdentByte(c)
		}
	case 1:
		switch {
		case c == '$':
			return true
		case reshapeASCIIAlpha(c) || c == '_' || c >= 0x80:
			d.state = 2
		default:
			d.state = 0
			d.prevIdent = reshapeDollarIdentByte(c)
		}
	case 2:
		switch {
		case c == '$':
			return true
		case reshapeASCIIWord(c):
		default:
			d.state = 0
			d.prevIdent = reshapeDollarIdentByte(c)
		}
	}
	return false
}

func (d *reshapeDollarQuoteDetector) Reset() { *d = reshapeDollarQuoteDetector{} }

func reshapeASCIIAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func reshapeASCIIWord(c byte) bool {
	return reshapeASCIIAlpha(c) || (c >= '0' && c <= '9') || c == '_' || c == '$' || c >= 0x80
}

func reshapeDollarIdentByte(c byte) bool { return reshapeASCIIWord(c) }

type reshapeClientHazard uint8

const (
	reshapeClientNoCommand reshapeClientHazard = iota
	reshapeClientBatchCommand
	reshapeClientDelimiterCommand
)

type reshapeStatementStarterClass uint8

const (
	reshapeStatementStarterUndecided reshapeStatementStarterClass = iota
	reshapeStatementStarterServerSQL
	reshapeStatementStarterUnknown
)

type reshapeClientLineState uint8

const (
	reshapeClientLineLeading reshapeClientLineState = iota
	reshapeClientLineToken
	reshapeClientLineAfterSet
	reshapeClientLineTermToken
	reshapeClientLineSlash
	reshapeClientLineSlashTrailing
	reshapeClientLineBang
	reshapeClientLineDot
	reshapeClientLineIgnored
)

// reshapeClientCommandDetector rejects line-oriented commands that alter
// statement boundaries outside SQL grammar. It receives whether each byte is
// live SQL so command-looking text inside quotes and comments remains payload.
// Only two constant-sized tokens are retained (the optional second one is TERM).
type reshapeClientCommandDetector struct {
	state                 reshapeClientLineState
	token                 [16]byte
	tokenLen              uint8
	overflow              bool
	ambiguousContinuation bool
	continuedStatement    bool
	capturingStarter      bool
	statementStarter      reshapeStatementStarterClass
	ambiguousSameLine     bool
}

func (d *reshapeClientCommandDetector) Step(c byte, live, statementHasContent, compoundPrefixIrrelevant bool) reshapeClientHazard {
	if c == '\r' || c == '\n' {
		hazard := d.finishLine()
		d.resetLine()
		return hazard
	}
	if !live {
		if !isHorizontalSpace(c) {
			d.state = reshapeClientLineIgnored
		}
		return reshapeClientNoCommand
	}
	if c == ';' {
		return d.finishStatementSegment(statementHasContent)
	}

	switch d.state {
	case reshapeClientLineLeading:
		if isHorizontalSpace(c) {
			return reshapeClientNoCommand
		}
		switch c {
		case '/':
			d.state = reshapeClientLineSlash
		case '\\':
			return reshapeClientBatchCommand
		case '@', '?':
			// SQL*Plus @file/@@file and mysql's ? help shorthand execute a
			// client-owned operation. A
			// semicolon later on the same physical command line is not a SQL
			// statement boundary and must never expose a phantom INSERT.
			// Inside an already-started SQL statement, however, line-leading @ and
			// ? are valid SQL Server variable / prepared-parameter syntax rather
			// than client commands.
			if !statementHasContent {
				return reshapeClientBatchCommand
			}
			d.state = reshapeClientLineIgnored
		case ':':
			// SQLCMD commands (other than GO) start with ':'. They are handled
			// by the client rather than SQL grammar and can otherwise hide a
			// following routine prefix until its first internal semicolon.
			return reshapeClientBatchCommand
		case '!':
			// SQLCMD's shell escape is introduced by "!!". Retain one byte of
			// fixed state so a single live SQL '!' remains a near miss.
			d.state = reshapeClientLineBang
		case '.':
			// sqlite3 dot commands start with a dot followed by an ASCII command
			// name. Delay the decision by one byte so a line-leading decimal such
			// as .5 remains ordinary SQL.
			d.state = reshapeClientLineDot
		default:
			if reshapeASCIIWord(c) {
				d.state = reshapeClientLineToken
				// An unknown first-line token can be a client command that consumes
				// the rest of that physical line without a SQL terminator (for
				// example SQL*Plus SPOOL). If the next physical line begins a new
				// statement, ordinary semicolon parsing cannot prove ownership.
				d.ambiguousContinuation = statementHasContent && compoundPrefixIrrelevant
				d.continuedStatement = statementHasContent
				d.capturingStarter = !statementHasContent && d.statementStarter == reshapeStatementStarterUndecided
				d.appendToken(c)
			} else {
				d.state = reshapeClientLineIgnored
			}
		}
	case reshapeClientLineToken:
		if reshapeASCIIWord(c) {
			d.appendToken(c)
			return reshapeClientNoCommand
		}
		return d.finishFirstToken(c)
	case reshapeClientLineAfterSet:
		if isHorizontalSpace(c) {
			return reshapeClientNoCommand
		}
		if reshapeASCIIWord(c) {
			d.clearToken()
			d.state = reshapeClientLineTermToken
			d.appendToken(c)
		} else {
			d.state = reshapeClientLineIgnored
		}
	case reshapeClientLineTermToken:
		if reshapeASCIIWord(c) {
			d.appendToken(c)
			return reshapeClientNoCommand
		}
		if d.tokenString() == "term" {
			return reshapeClientDelimiterCommand
		}
		d.state = reshapeClientLineIgnored
	case reshapeClientLineSlash:
		if isHorizontalSpace(c) {
			d.state = reshapeClientLineSlashTrailing
		} else {
			d.state = reshapeClientLineIgnored
		}
	case reshapeClientLineSlashTrailing:
		if !isHorizontalSpace(c) {
			d.state = reshapeClientLineIgnored
		}
	case reshapeClientLineBang:
		if c == '!' || reshapeASCIIAlpha(c) {
			// SQLCMD uses "!!" for shell escapes. SnowSQL uses ! followed by
			// an ASCII command name (!set, !source, !spool, ...).
			return reshapeClientBatchCommand
		}
		d.state = reshapeClientLineIgnored
	case reshapeClientLineDot:
		if reshapeASCIIAlpha(c) {
			return reshapeClientBatchCommand
		}
		d.state = reshapeClientLineIgnored
	}
	return reshapeClientNoCommand
}

func (d *reshapeClientCommandDetector) finishFirstToken(boundary byte) reshapeClientHazard {
	token := d.tokenString()
	if d.capturingStarter {
		if reshapeKnownServerStatementStarter(token) {
			d.statementStarter = reshapeStatementStarterServerSQL
		} else {
			d.statementStarter = reshapeStatementStarterUnknown
		}
		d.capturingStarter = false
	}
	if reshapeAlwaysClientToken(token) || (!d.continuedStatement && reshapeTopLevelClientToken(token)) {
		// These file, shell, buffer, and script commands are owned by SQL
		// clients rather than server SQL when they begin a top-level line. A
		// later semicolon on that physical line must not expose executable SQL
		// bytes; an already-started multiline SQL statement remains supported.
		return reshapeClientBatchCommand
	}
	switch token {
	case "set":
		if isHorizontalSpace(boundary) {
			d.state = reshapeClientLineAfterSet
			d.clearToken()
			return reshapeClientNoCommand
		}
	}
	if (d.ambiguousContinuation || d.ambiguousSameLine) && reshapeUnsafeContinuationStarter(token) {
		return reshapeClientBatchCommand
	}
	d.state = reshapeClientLineIgnored
	return reshapeClientNoCommand
}

// reshapeKnownServerStatementStarter is intentionally conservative. Tokens
// shared by server SQL and client command languages (for example SET, START,
// USE, CALL, EXECUTE, COPY, LOAD, and SHOW) stay unknown: when they precede a
// same-line semicolon and target statement, refusing reshape is safer than
// treating client-owned bytes as independent SQL.
func reshapeKnownServerStatementStarter(token string) bool {
	switch token {
	case "abort", "alter", "analyze", "attach", "audit", "backup", "begin", "binlog", "bulk", "cache",
		"check", "checkpoint", "checksum", "clone", "close", "cluster", "comment", "commit", "create",
		"dbcc", "deallocate", "declare", "delete", "deny", "detach", "discard", "disassociate", "do",
		"drop", "explain", "export", "fetch", "flashback", "flush", "grant", "handler", "import",
		"insert", "install", "kill", "listen", "lock", "merge", "move", "noaudit", "notify", "optimize",
		"pivot", "pragma", "prepare", "purge", "reassign", "refresh", "reindex", "release", "remove",
		"rename", "repair", "replace", "reset", "restart", "restore", "revoke", "rollback", "savepoint",
		"security", "select", "summarize", "truncate", "uncache", "undrop", "uninstall",
		"unlisten", "unlock", "unpivot", "update", "upsert", "vacuum", "values", "with":
		return true
	default:
		return false
	}
}

func (d *reshapeClientCommandDetector) finishStatementSegment(statementHasContent bool) reshapeClientHazard {
	var hazard reshapeClientHazard
	switch d.state {
	case reshapeClientLineToken:
		hazard = d.finishFirstToken(';')
	case reshapeClientLineTermToken:
		if d.tokenString() == "term" {
			hazard = reshapeClientDelimiterCommand
		}
	}
	if hazard != reshapeClientNoCommand {
		return hazard
	}
	if statementHasContent {
		d.ambiguousSameLine = d.ambiguousSameLine || d.statementStarter == reshapeStatementStarterUnknown
	}
	d.statementStarter = reshapeStatementStarterUndecided
	d.resetSegment()
	return reshapeClientNoCommand
}

func (d *reshapeClientCommandDetector) resetSegment() {
	d.state = reshapeClientLineLeading
	d.ambiguousContinuation = false
	d.continuedStatement = false
	d.capturingStarter = false
	d.clearToken()
}

func (d *reshapeClientCommandDetector) ResumeAfterLeadingTrivia() { d.resetSegment() }

func reshapeAlwaysClientToken(token string) bool {
	switch token {
	case "go", "prompt", "rem", "remark":
		return true
	default:
		return false
	}
}

func reshapeTopLevelClientToken(token string) bool {
	switch token {
	case "spool", "source", "host", "system", "tee", "pager", "input", "output":
		return true
	default:
		return false
	}
}

func reshapeUnsafeContinuationStarter(token string) bool {
	switch token {
	case "insert", "create", "alter", "recreate", "replace", "begin", "declare", "do", "execute",
		"copy", "if", "loop", "while", "for", "repeat":
		return true
	default:
		return false
	}
}

func (d *reshapeClientCommandDetector) finishLine() reshapeClientHazard {
	switch d.state {
	case reshapeClientLineToken:
		if reshapeAlwaysClientToken(d.tokenString()) ||
			(!d.continuedStatement && reshapeTopLevelClientToken(d.tokenString())) {
			return reshapeClientBatchCommand
		}
		return d.finishFirstToken('\n')
	case reshapeClientLineTermToken:
		if d.tokenString() == "term" {
			return reshapeClientDelimiterCommand
		}
	case reshapeClientLineSlash, reshapeClientLineSlashTrailing:
		return reshapeClientBatchCommand
	}
	return reshapeClientNoCommand
}

func (d *reshapeClientCommandDetector) Finish() reshapeClientHazard { return d.finishLine() }

func (d *reshapeClientCommandDetector) appendToken(c byte) {
	if int(d.tokenLen) < len(d.token) {
		d.token[d.tokenLen] = lower(c)
		d.tokenLen++
	} else {
		d.overflow = true
	}
}

func (d *reshapeClientCommandDetector) tokenString() string {
	if d.overflow {
		return ""
	}
	return string(d.token[:d.tokenLen])
}

func (d *reshapeClientCommandDetector) clearToken() {
	d.tokenLen = 0
	d.overflow = false
}

func (d *reshapeClientCommandDetector) resetLine() {
	d.resetSegment()
	d.ambiguousSameLine = false
}

func isHorizontalSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\f' || c == '\v'
}
