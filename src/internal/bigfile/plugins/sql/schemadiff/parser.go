package schemadiff

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unsafe"
)

// ParseColumns retains the column-only compatibility API. Call ParseTable when
// completeness, constraints, indexes, and options are required for comparison.
func ParseColumns(ddl []byte) []Column {
	parsed := ParseTable(ddl)
	if parsed.Status != ParseComplete {
		return nil
	}
	return parsed.Items
}

// ParseTable parses one complete, semicolon-terminated CREATE TABLE statement.
// It never returns ParseComplete for truncated, over-budget, or unsupported DDL.
func ParseTable(ddl []byte) ParsedTable {
	parsed, _ := ParseTableContext(context.Background(), ddl)
	return parsed
}

// ParseTableContext is the cancellable form of ParseTable. SQL syntax and
// completeness failures remain explicit ParseUnknown results; cancellation is
// returned separately so callers cannot publish a partial comparison as an
// ordinary unknown schema.
func ParseTableContext(ctx context.Context, ddl []byte) (ParsedTable, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ParsedTable{}, err
	}
	if len(ddl) > MaxStatementBytes {
		return unknownParsedTable(fmt.Sprintf("CREATE TABLE statement exceeds %d-byte limit", MaxStatementBytes)), nil
	}
	parsed, err := parseTableDDLContext(ctx, ddl)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ParsedTable{}, ctxErr
		}
		return unknownParsedTable(err.Error()), nil
	}
	if err := ctx.Err(); err != nil {
		return ParsedTable{}, err
	}
	return parsed, nil
}

// ParseTableReader is the chunk-independent bounded reader form of ParseTable.
// At most MaxStatementBytes+1 bytes are read, regardless of reader behavior.
func ParseTableReader(reader io.Reader) ParsedTable {
	if reader == nil {
		return unknownParsedTable("CREATE TABLE reader is nil")
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxStatementBytes+1))
	if err != nil {
		return unknownParsedTable("read CREATE TABLE statement: " + err.Error())
	}
	return ParseTable(data)
}

// ParsedTableRetainedBytes returns a conservative charge for the parsed
// table's retained slice backing and string data. It intentionally includes
// private canonical forms because service-level aggregate admission must bound
// actual retained parser state, not only the JSON-visible definitions.
func ParsedTableRetainedBytes(parsed ParsedTable) int64 {
	total := uint64(unsafe.Sizeof(parsed))
	add := func(value uint64) {
		if value > math.MaxInt64 || total > math.MaxInt64-value {
			total = math.MaxInt64
			return
		}
		total += value
	}
	add(uint64(cap(parsed.Items)) * uint64(unsafe.Sizeof(Column{})))
	add(uint64(cap(parsed.Constraints)) * uint64(unsafe.Sizeof(SchemaElement{})))
	add(uint64(cap(parsed.Indexes)) * uint64(unsafe.Sizeof(SchemaElement{})))
	add(uint64(cap(parsed.Options)) * uint64(unsafe.Sizeof(TableOption{})))
	for _, value := range []string{parsed.Reason, parsed.tableIdentity, parsed.tableDisplay, parsed.modifiers} {
		add(uint64(len(value)))
	}
	for _, column := range parsed.Items {
		add(uint64(len(column.Name) + len(column.Definition) + len(column.canonical) + len(column.identity)))
	}
	for _, constraint := range parsed.Constraints {
		add(uint64(len(constraint.Definition) + len(constraint.canonical)))
	}
	for _, index := range parsed.Indexes {
		add(uint64(len(index.Definition) + len(index.canonical)))
	}
	for _, option := range parsed.Options {
		add(uint64(len(option.Name) + len(option.Value) + len(option.canonical)))
	}
	return int64(total)
}

// TableRetainedBytes extends ParsedTableRetainedBytes with the outer table and
// compatibility-column storage retained by Diff.
func TableRetainedBytes(table Table) int64 {
	total := uint64(ParsedTableRetainedBytes(table.Definition))
	add := func(value uint64) {
		if value > math.MaxInt64 || total > math.MaxInt64-value {
			total = math.MaxInt64
			return
		}
		total += value
	}
	add(uint64(unsafe.Sizeof(table)))
	add(uint64(len(table.Name)))
	add(uint64(cap(table.Columns)) * uint64(unsafe.Sizeof(Column{})))
	for _, column := range table.Columns {
		add(uint64(len(column.Name) + len(column.Definition) + len(column.canonical) + len(column.identity)))
	}
	return int64(total)
}

func unknownParsedTable(reason string) ParsedTable {
	return ParsedTable{
		Items:       []Column{},
		Constraints: []SchemaElement{},
		Indexes:     []SchemaElement{},
		Options:     []TableOption{},
		Status:      ParseUnknown,
		Reason:      reason,
	}
}

func newParsedTable() ParsedTable {
	return ParsedTable{
		Items:       []Column{},
		Constraints: []SchemaElement{},
		Indexes:     []SchemaElement{},
		Options:     []TableOption{},
		Status:      ParseComplete,
		parsed:      true,
	}
}

func parseTableDDLContext(ctx context.Context, ddl []byte) (ParsedTable, error) {
	if err := ctx.Err(); err != nil {
		return ParsedTable{}, err
	}
	lexed, err := lexDDLContext(ctx, ddl)
	if err != nil {
		return ParsedTable{}, err
	}
	if err := ctx.Err(); err != nil {
		return ParsedTable{}, err
	}
	if lexed.warning != "" {
		return ParsedTable{}, fmt.Errorf("unsupported dialect ambiguity: %s", lexed.warning)
	}
	tokens := lexed.tokens
	if len(tokens) == 0 {
		return ParsedTable{}, fmt.Errorf("CREATE TABLE statement is empty")
	}

	parsed := newParsedTable()
	pos, modifiers, err := parseCreateTablePrefix(tokens)
	if err != nil {
		return ParsedTable{}, err
	}
	parsed.modifiers = modifiers
	identity, display, next, err := parseIdentifierPath(tokens, pos)
	if err != nil {
		return ParsedTable{}, err
	}
	parsed.tableIdentity = identity
	parsed.tableDisplay = display
	pos = next
	if pos >= len(tokens) || tokens[pos].raw != "(" {
		return ParsedTable{}, fmt.Errorf("unsupported CREATE TABLE form: expected a parenthesized table definition")
	}
	bodyClose, err := matchingParen(tokens, pos)
	if err != nil {
		return ParsedTable{}, err
	}
	if len(tokens) == 0 || tokens[len(tokens)-1].raw != ";" {
		return ParsedTable{}, fmt.Errorf("CREATE TABLE statement is incomplete or lacks a terminating semicolon")
	}
	for i := bodyClose + 1; i < len(tokens)-1; i++ {
		if i%schemaContextCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return ParsedTable{}, err
			}
		}
		if tokens[i].raw == ";" {
			return ParsedTable{}, fmt.Errorf("multiple statements or an early terminator are unsupported")
		}
	}

	entries, err := splitBodyEntries(tokens[pos+1 : bodyClose])
	if err != nil {
		return ParsedTable{}, err
	}
	if len(entries) > MaxSchemaEntryCount {
		return ParsedTable{}, fmt.Errorf("schema entry count exceeds limit of %d", MaxSchemaEntryCount)
	}
	for i, entry := range entries {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return ParsedTable{}, err
			}
		}
		if err := appendSchemaEntry(&parsed, entry); err != nil {
			return ParsedTable{}, err
		}
	}
	if len(parsed.Items) > MaxColumnCount {
		return ParsedTable{}, fmt.Errorf("column count exceeds limit of %d", MaxColumnCount)
	}
	if _, duplicate := indexColumns(parsed.Items); duplicate != "" {
		return ParsedTable{}, fmt.Errorf("duplicate column identity %q is unsupported", duplicate)
	}

	options, err := parseTableOptions(tokens[bodyClose+1 : len(tokens)-1])
	if err != nil {
		return ParsedTable{}, err
	}
	parsed.Options = options
	if err := ctx.Err(); err != nil {
		return ParsedTable{}, err
	}
	parsed.integrity = parsedTableIntegrity(parsed)
	if err := ctx.Err(); err != nil {
		return ParsedTable{}, err
	}
	return parsed, nil
}

func parseCreateTablePrefix(tokens []sqlToken) (int, string, error) {
	pos := 0
	if !tokenIsKeywordAt(tokens, pos, "create") {
		return 0, "", fmt.Errorf("expected CREATE TABLE statement")
	}
	pos++
	if tokenIsKeywordAt(tokens, pos, "or") && tokenIsKeywordAt(tokens, pos+1, "replace") {
		pos += 2
	}
	modifiers := make([]string, 0, 2)
	for pos < len(tokens) {
		switch {
		case tokenIsKeywordAt(tokens, pos, "temporary") || tokenIsKeywordAt(tokens, pos, "temp"):
			modifiers = append(modifiers, "temporary")
			pos++
		case tokenIsKeywordAt(tokens, pos, "unlogged"):
			modifiers = append(modifiers, "unlogged")
			pos++
		case (tokenIsKeywordAt(tokens, pos, "global") || tokenIsKeywordAt(tokens, pos, "local")) && tokenIsKeywordAt(tokens, pos+1, "temporary"):
			modifiers = append(modifiers, strings.ToLower(tokens[pos].raw)+" temporary")
			pos += 2
		default:
			goto modifiersDone
		}
	}

modifiersDone:
	if !tokenIsKeywordAt(tokens, pos, "table") {
		return 0, "", fmt.Errorf("unsupported CREATE form: expected TABLE keyword")
	}
	pos++
	if tokenIsKeywordAt(tokens, pos, "if") && tokenIsKeywordAt(tokens, pos+1, "not") && tokenIsKeywordAt(tokens, pos+2, "exists") {
		pos += 3
	}
	sort.Strings(modifiers)
	return pos, strings.Join(modifiers, " "), nil
}

func parseIdentifierPath(tokens []sqlToken, pos int) (identity, display string, next int, err error) {
	parts := make([]string, 0, 2)
	rawParts := make([]string, 0, 2)
	partKeys := make([]string, 0, 2)
	for {
		if pos >= len(tokens) || !isIdentifierToken(tokens[pos]) {
			return "", "", pos, fmt.Errorf("expected table identifier")
		}
		value := identifierValue(tokens[pos])
		if value == "" {
			return "", "", pos, fmt.Errorf("empty table identifier is unsupported")
		}
		if len(value) > MaxIdentifierBytes {
			return "", "", pos, fmt.Errorf("table identifier exceeds %d bytes", MaxIdentifierBytes)
		}
		parts = append(parts, value)
		rawParts = append(rawParts, tokens[pos].raw)
		partKeys = append(partKeys, identifierTokenKey(tokens[pos]))
		pos++
		if pos >= len(tokens) || tokens[pos].raw != "." {
			break
		}
		pos++
		if len(parts) >= 3 {
			return "", "", pos, fmt.Errorf("table identifiers with more than three qualification segments are unsupported")
		}
	}
	var key strings.Builder
	for _, partKey := range partKeys {
		key.WriteString(strconv.Itoa(len(partKey)))
		key.WriteByte(':')
		key.WriteString(partKey)
		key.WriteByte('|')
	}
	return key.String(), strings.Join(rawParts, "."), pos, nil
}

func matchingParen(tokens []sqlToken, open int) (int, error) {
	if open >= len(tokens) || tokens[open].raw != "(" {
		return 0, fmt.Errorf("expected opening parenthesis")
	}
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch tokens[i].raw {
		case "(":
			depth++
			if depth > MaxNestingDepth {
				return 0, fmt.Errorf("DDL nesting exceeds limit of %d", MaxNestingDepth)
			}
		case ")":
			depth--
			if depth == 0 {
				return i, nil
			}
			if depth < 0 {
				return 0, fmt.Errorf("unexpected closing parenthesis")
			}
		}
	}
	return 0, fmt.Errorf("truncated CREATE TABLE body: closing parenthesis is missing")
}

func splitBodyEntries(tokens []sqlToken) ([][]sqlToken, error) {
	if len(tokens) == 0 {
		return [][]sqlToken{}, nil
	}
	entries := make([][]sqlToken, 0, minInt(len(tokens)/3+1, MaxSchemaEntryCount))
	start := 0
	depth := 0
	for i, token := range tokens {
		switch token.raw {
		case "(":
			depth++
			if depth > MaxNestingDepth {
				return nil, fmt.Errorf("schema entry nesting exceeds limit of %d", MaxNestingDepth)
			}
		case ")":
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced schema entry parenthesis")
			}
		case ",":
			if depth == 0 {
				if i == start {
					return nil, fmt.Errorf("empty schema entry is unsupported")
				}
				entries = append(entries, tokens[start:i])
				if len(entries) > MaxSchemaEntryCount {
					return nil, fmt.Errorf("schema entry count exceeds limit of %d", MaxSchemaEntryCount)
				}
				start = i + 1
			}
		case ";":
			if depth == 0 {
				return nil, fmt.Errorf("statement terminator inside CREATE TABLE body")
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced schema entry parenthesis")
	}
	if start == len(tokens) {
		return nil, fmt.Errorf("trailing comma leaves an incomplete schema entry")
	}
	entries = append(entries, tokens[start:])
	return entries, nil
}

type schemaEntryKind uint8

const (
	entryColumn schemaEntryKind = iota + 1
	entryConstraint
	entryIndex
)

func appendSchemaEntry(parsed *ParsedTable, entry []sqlToken) error {
	if len(entry) == 0 {
		return fmt.Errorf("empty schema entry")
	}
	if definitionSpan(entry) > MaxDefinitionBytes {
		return fmt.Errorf("schema entry exceeds %d-byte definition limit", MaxDefinitionBytes)
	}
	kind, err := classifySchemaEntry(entry)
	if err != nil {
		return err
	}
	if kind == entryColumn {
		column, err := parseColumn(entry)
		if err != nil {
			return err
		}
		parsed.Items = append(parsed.Items, column)
		if len(parsed.Items) > MaxColumnCount {
			return fmt.Errorf("column count exceeds limit of %d", MaxColumnCount)
		}
		return nil
	}
	if err := validateTableElement(entry, kind); err != nil {
		return err
	}
	element := SchemaElement{Definition: renderTokens(entry), canonical: canonicalTokens(entry, false)}
	if kind == entryIndex {
		parsed.Indexes = append(parsed.Indexes, element)
	} else {
		parsed.Constraints = append(parsed.Constraints, element)
	}
	return nil
}

func classifySchemaEntry(entry []sqlToken) (schemaEntryKind, error) {
	first := entry[0]
	if first.kind == tokenQuotedIdentifier {
		return entryColumn, nil
	}
	if first.kind != tokenWord {
		return 0, fmt.Errorf("schema entry at offset %d does not begin with an identifier", first.start)
	}
	switch strings.ToLower(first.raw) {
	case "primary", "foreign", "check", "constraint":
		return entryConstraint, nil
	case "key", "index", "fulltext", "spatial":
		return entryIndex, nil
	case "unique":
		if tokenIsKeywordAt(entry, 1, "key") || tokenIsKeywordAt(entry, 1, "index") {
			return entryIndex, nil
		}
		return entryConstraint, nil
	case "like", "exclude", "period":
		return 0, fmt.Errorf("unsupported table-level schema clause %q", first.raw)
	default:
		return entryColumn, nil
	}
}

func parseColumn(entry []sqlToken) (Column, error) {
	if !isIdentifierToken(entry[0]) {
		return Column{}, fmt.Errorf("column entry does not begin with an identifier")
	}
	// Bare identifier tokens are substrings of the lexer's statement-sized
	// input string. Clone the name before retaining it in ParsedTable so a tiny
	// column name cannot keep an otherwise transient multi-megabyte DDL backing
	// allocation alive while the service charges only the name's visible bytes.
	name := strings.Clone(identifierValue(entry[0]))
	if name == "" {
		return Column{}, fmt.Errorf("empty column identifier is unsupported")
	}
	definition := entry[1:]
	if len(definition) == 0 {
		return Column{}, fmt.Errorf("column %q has no type or definition", name)
	}
	if definition[0].kind != tokenWord && definition[0].kind != tokenQuotedIdentifier {
		return Column{}, fmt.Errorf("column %q has an unsupported type token %q", name, definition[0].raw)
	}
	if err := validateColumnDefinition(name, definition); err != nil {
		return Column{}, err
	}
	return Column{
		Name:       name,
		Definition: renderTokens(definition),
		canonical:  canonicalTokens(definition, false),
		identity:   identifierTokenKey(entry[0]),
	}, nil
}

func validateColumnDefinition(name string, definition []sqlToken) error {
	pos, err := consumeColumnType(definition)
	if err != nil {
		return fmt.Errorf("column %q: %w", name, err)
	}
	for pos < len(definition) {
		next, err := consumeColumnClause(definition, pos)
		if err != nil {
			return fmt.Errorf("column %q: %w", name, err)
		}
		if next <= pos {
			return fmt.Errorf("column %q: parser made no progress near %q", name, definition[pos].raw)
		}
		pos = next
	}
	return nil
}

func consumeColumnType(tokens []sqlToken) (int, error) {
	if len(tokens) == 0 || tokens[0].kind != tokenWord {
		return 0, fmt.Errorf("unsupported or missing column type")
	}
	first := strings.ToLower(tokens[0].raw)
	if !knownTypeWords[first] {
		return 0, fmt.Errorf("unsupported column type %q", tokens[0].raw)
	}
	pos := 1
	for pos < len(tokens) {
		if tokens[pos].raw == "(" {
			close, err := matchingParen(tokens, pos)
			if err != nil {
				return 0, fmt.Errorf("type arguments: %w", err)
			}
			pos = close + 1
			continue
		}
		if tokens[pos].kind != tokenWord {
			break
		}
		word := strings.ToLower(tokens[pos].raw)
		if isColumnClauseStart(tokens, pos) {
			break
		}
		if !typeContinuationWords[word] {
			return 0, fmt.Errorf("unsupported type modifier or clause %q", tokens[pos].raw)
		}
		pos++
	}
	return pos, nil
}

func consumeColumnClause(tokens []sqlToken, pos int) (int, error) {
	if pos >= len(tokens) || tokens[pos].kind != tokenWord {
		return 0, fmt.Errorf("unsupported column clause near %q", tokens[pos].raw)
	}
	switch strings.ToLower(tokens[pos].raw) {
	case "not":
		if !tokenIsKeywordAt(tokens, pos+1, "null") {
			return 0, fmt.Errorf("unsupported NOT clause")
		}
		return consumeOptionalOnConflict(tokens, pos+2)
	case "null":
		return consumeOptionalOnConflict(tokens, pos+1)
	case "default":
		next, err := consumeDefaultExpression(tokens, pos+1)
		if err != nil {
			return 0, err
		}
		return next, nil
	case "primary":
		if !tokenIsKeywordAt(tokens, pos+1, "key") {
			return 0, fmt.Errorf("malformed PRIMARY KEY clause")
		}
		next := pos + 2
		for next < len(tokens) && (tokenIsKeywordAt(tokens, next, "asc") || tokenIsKeywordAt(tokens, next, "desc") || tokenIsKeywordAt(tokens, next, "autoincrement") || tokenIsKeywordAt(tokens, next, "auto_increment")) {
			next++
		}
		return consumeOptionalOnConflict(tokens, next)
	case "unique":
		next := pos + 1
		if tokenIsKeywordAt(tokens, next, "key") {
			next++
		}
		if tokenIsKeywordAt(tokens, next, "nulls") {
			next++
			if tokenIsKeywordAt(tokens, next, "not") {
				next++
			}
			if !tokenIsKeywordAt(tokens, next, "distinct") {
				return 0, fmt.Errorf("malformed UNIQUE NULLS clause")
			}
			next++
		}
		return consumeOptionalOnConflict(tokens, next)
	case "references":
		return consumeReferencesClause(tokens, pos+1)
	case "check":
		if pos+1 >= len(tokens) || tokens[pos+1].raw != "(" {
			return 0, fmt.Errorf("malformed CHECK clause")
		}
		close, err := matchingParen(tokens, pos+1)
		if err != nil {
			return 0, fmt.Errorf("CHECK clause: %w", err)
		}
		return consumeOptionalOnConflict(tokens, close+1)
	case "constraint":
		if pos+2 >= len(tokens) || !isIdentifierToken(tokens[pos+1]) {
			return 0, fmt.Errorf("truncated named constraint")
		}
		return consumeColumnClause(tokens, pos+2)
	case "generated":
		return consumeGeneratedClause(tokens, pos+1)
	case "as":
		return consumeGeneratedExpression(tokens, pos+1)
	case "collate":
		if pos+1 >= len(tokens) || !isIdentifierToken(tokens[pos+1]) {
			return 0, fmt.Errorf("truncated COLLATE clause")
		}
		return pos + 2, nil
	case "character":
		if !tokenIsKeywordAt(tokens, pos+1, "set") || pos+2 >= len(tokens) || !isIdentifierToken(tokens[pos+2]) {
			return 0, fmt.Errorf("truncated CHARACTER SET clause")
		}
		return pos + 3, nil
	case "auto_increment", "autoincrement", "visible", "invisible":
		return pos + 1, nil
	case "comment":
		if pos+1 >= len(tokens) || tokens[pos+1].kind != tokenString {
			return 0, fmt.Errorf("malformed COMMENT clause")
		}
		return pos + 2, nil
	case "on":
		if !tokenIsKeywordAt(tokens, pos+1, "update") {
			return 0, fmt.Errorf("unsupported ON clause")
		}
		return consumeDefaultExpression(tokens, pos+2)
	case "deferrable":
		return consumeOptionalInitially(tokens, pos+1)
	case "initially":
		if !tokenIsKeywordAt(tokens, pos+1, "deferred") && !tokenIsKeywordAt(tokens, pos+1, "immediate") {
			return 0, fmt.Errorf("malformed INITIALLY clause")
		}
		return pos + 2, nil
	case "column_format", "storage", "srid", "compression", "encoding", "statistics":
		if pos+1 >= len(tokens) || isColumnClauseStart(tokens, pos+1) {
			return 0, fmt.Errorf("truncated %s clause", strings.ToUpper(tokens[pos].raw))
		}
		return pos + 2, nil
	default:
		return 0, fmt.Errorf("unsupported column clause %q", tokens[pos].raw)
	}
}

func consumeDefaultExpression(tokens []sqlToken, pos int) (int, error) {
	if pos >= len(tokens) {
		return 0, fmt.Errorf("truncated expression")
	}
	if tokens[pos].raw == "+" || tokens[pos].raw == "-" {
		pos++
		if pos >= len(tokens) {
			return 0, fmt.Errorf("truncated signed expression")
		}
	}
	if tokens[pos].raw == "(" {
		close, err := matchingParen(tokens, pos)
		if err != nil {
			return 0, fmt.Errorf("expression: %w", err)
		}
		return close + 1, nil
	}
	if tokens[pos].kind != tokenWord && tokens[pos].kind != tokenQuotedIdentifier && tokens[pos].kind != tokenString && tokens[pos].kind != tokenNumber {
		return 0, fmt.Errorf("unsupported expression token %q", tokens[pos].raw)
	}
	next := pos + 1
	if tokens[pos].kind == tokenWord && isLiteralPrefix(tokens[pos].raw) && next < len(tokens) && tokens[next].kind == tokenString {
		next++
	}
	if next < len(tokens) && tokens[next].raw == "(" {
		close, err := matchingParen(tokens, next)
		if err != nil {
			return 0, fmt.Errorf("function expression: %w", err)
		}
		next = close + 1
	}
	if next < len(tokens) && tokens[next].raw == "::" {
		next++
		if next >= len(tokens) || !isIdentifierToken(tokens[next]) {
			return 0, fmt.Errorf("truncated cast expression")
		}
		next++
	}
	return next, nil
}

func consumeGeneratedClause(tokens []sqlToken, pos int) (int, error) {
	if tokenIsKeywordAt(tokens, pos, "always") {
		pos++
	} else if tokenIsKeywordAt(tokens, pos, "by") && tokenIsKeywordAt(tokens, pos+1, "default") {
		pos += 2
	}
	if !tokenIsKeywordAt(tokens, pos, "as") {
		return 0, fmt.Errorf("truncated GENERATED clause")
	}
	pos++
	if tokenIsKeywordAt(tokens, pos, "identity") {
		pos++
		if pos < len(tokens) && tokens[pos].raw == "(" {
			close, err := matchingParen(tokens, pos)
			if err != nil {
				return 0, fmt.Errorf("identity options: %w", err)
			}
			pos = close + 1
		}
		return pos, nil
	}
	return consumeGeneratedExpression(tokens, pos)
}

func consumeGeneratedExpression(tokens []sqlToken, pos int) (int, error) {
	if pos >= len(tokens) || tokens[pos].raw != "(" {
		return 0, fmt.Errorf("generated expression must be parenthesized")
	}
	close, err := matchingParen(tokens, pos)
	if err != nil {
		return 0, fmt.Errorf("generated expression: %w", err)
	}
	next := close + 1
	if tokenIsKeywordAt(tokens, next, "stored") || tokenIsKeywordAt(tokens, next, "virtual") {
		next++
	}
	return next, nil
}

func consumeReferencesClause(tokens []sqlToken, pos int) (int, error) {
	_, _, next, err := parseIdentifierPath(tokens, pos)
	if err != nil {
		return 0, fmt.Errorf("REFERENCES clause: %w", err)
	}
	if next < len(tokens) && tokens[next].raw == "(" {
		close, err := matchingParen(tokens, next)
		if err != nil {
			return 0, fmt.Errorf("REFERENCES columns: %w", err)
		}
		next = close + 1
	}
	for next < len(tokens) {
		switch {
		case tokenIsKeywordAt(tokens, next, "match"):
			if next+1 >= len(tokens) || !tokenIsKeywordAt(tokens, next+1, "full") && !tokenIsKeywordAt(tokens, next+1, "partial") && !tokenIsKeywordAt(tokens, next+1, "simple") {
				return 0, fmt.Errorf("malformed REFERENCES MATCH clause")
			}
			next += 2
		case tokenIsKeywordAt(tokens, next, "on"):
			if !tokenIsKeywordAt(tokens, next+1, "delete") && !tokenIsKeywordAt(tokens, next+1, "update") {
				return 0, fmt.Errorf("malformed REFERENCES ON clause")
			}
			next += 2
			var err error
			next, err = consumeReferenceAction(tokens, next)
			if err != nil {
				return 0, err
			}
		case tokenIsKeywordAt(tokens, next, "not") && tokenIsKeywordAt(tokens, next+1, "deferrable"):
			next += 2
			next, err = consumeOptionalInitially(tokens, next)
			if err != nil {
				return 0, err
			}
		case tokenIsKeywordAt(tokens, next, "deferrable"):
			next++
			next, err = consumeOptionalInitially(tokens, next)
			if err != nil {
				return 0, err
			}
		case tokenIsKeywordAt(tokens, next, "initially"):
			next, err = consumeOptionalInitially(tokens, next)
			if err != nil {
				return 0, err
			}
		default:
			return next, nil
		}
	}
	return next, nil
}

func consumeReferenceAction(tokens []sqlToken, pos int) (int, error) {
	if tokenIsKeywordAt(tokens, pos, "cascade") || tokenIsKeywordAt(tokens, pos, "restrict") {
		return pos + 1, nil
	}
	if tokenIsKeywordAt(tokens, pos, "set") && (tokenIsKeywordAt(tokens, pos+1, "null") || tokenIsKeywordAt(tokens, pos+1, "default")) {
		return pos + 2, nil
	}
	if tokenIsKeywordAt(tokens, pos, "no") && tokenIsKeywordAt(tokens, pos+1, "action") {
		return pos + 2, nil
	}
	return 0, fmt.Errorf("unsupported REFERENCES action")
}

func consumeOptionalOnConflict(tokens []sqlToken, pos int) (int, error) {
	if !tokenIsKeywordAt(tokens, pos, "on") || !tokenIsKeywordAt(tokens, pos+1, "conflict") {
		return pos, nil
	}
	if pos+2 >= len(tokens) || !isIdentifierToken(tokens[pos+2]) {
		return 0, fmt.Errorf("malformed ON CONFLICT clause")
	}
	return pos + 3, nil
}

func consumeOptionalInitially(tokens []sqlToken, pos int) (int, error) {
	if !tokenIsKeywordAt(tokens, pos, "initially") {
		return pos, nil
	}
	if !tokenIsKeywordAt(tokens, pos+1, "deferred") && !tokenIsKeywordAt(tokens, pos+1, "immediate") {
		return 0, fmt.Errorf("malformed INITIALLY clause")
	}
	return pos + 2, nil
}

func isColumnClauseStart(tokens []sqlToken, pos int) bool {
	if pos < 0 || pos >= len(tokens) || tokens[pos].kind != tokenWord {
		return false
	}
	word := strings.ToLower(tokens[pos].raw)
	if word == "character" && !tokenIsKeywordAt(tokens, pos+1, "set") {
		return false
	}
	return columnClauseWords[word]
}

func isLiteralPrefix(value string) bool {
	switch strings.ToLower(value) {
	case "b", "e", "n", "x":
		return true
	default:
		return false
	}
}

var knownTypeWords = wordSet(`
	bigint bigserial binary bit blob bool boolean box bytea char character cidr clob date
	datetime dec decimal double enum float geometry geometrycollection inet int integer interval
	json jsonb line linestring long longblob longtext macaddr mediumblob mediumint mediumtext money
	national nchar numeric nvarchar point polygon real serial set smallint smallserial text time
	timestamp tinyblob tinyint tinytext uuid varbinary varchar xml year
`)

var typeContinuationWords = wordSet(`
	array binary character day hour int minute month precision signed second time to unsigned
	varchar varying with without year zerofill zone
`)

var columnClauseWords = wordSet(`
	as auto_increment autoincrement character check collate column_format comment compression
	constraint default deferrable encoding generated initially invisible not null on primary
	references srid statistics storage unique visible
`)

var tableElementKeywords = wordSet(`
	action asc btree cascade check comment conflict constraint default deferrable deferred delete
	desc distinct foreign full fulltext hash immediate include index initially inherit invisible key
	match no not null nulls on parser partial primary references restrict set simple spatial unique
	update using visible where with
`)

func wordSet(words string) map[string]bool {
	set := make(map[string]bool)
	for word := range strings.FieldsSeq(words) {
		set[word] = true
	}
	return set
}

func validateTableElement(entry []sqlToken, kind schemaEntryKind) error {
	start := 0
	if tokenIsKeywordAt(entry, 0, "constraint") {
		if len(entry) < 3 || !isIdentifierToken(entry[1]) {
			return fmt.Errorf("named CONSTRAINT is truncated")
		}
		start = 2
	}
	if start >= len(entry) || entry[start].kind != tokenWord {
		return fmt.Errorf("table constraint type is missing")
	}
	first := strings.ToLower(entry[start].raw)
	switch first {
	case "primary":
		if !tokenIsKeywordAt(entry, start+1, "key") {
			return fmt.Errorf("PRIMARY KEY constraint is truncated")
		}
		return validateIndexElement(entry, start+2, false)
	case "foreign":
		if !tokenIsKeywordAt(entry, start+1, "key") {
			return fmt.Errorf("FOREIGN KEY constraint is truncated")
		}
		return validateForeignKeyElement(entry, start+2)
	case "check":
		return validateCheckElement(entry, start+1)
	case "unique":
		next := start + 1
		allowName := false
		if tokenIsKeywordAt(entry, next, "key") || tokenIsKeywordAt(entry, next, "index") {
			next++
			allowName = true
		}
		return validateIndexElement(entry, next, allowName)
	case "key", "index", "fulltext", "spatial":
		if kind != entryIndex {
			return fmt.Errorf("index definition is truncated")
		}
		next := start + 1
		if first == "fulltext" || first == "spatial" {
			if tokenIsKeywordAt(entry, next, "key") || tokenIsKeywordAt(entry, next, "index") {
				next++
			}
		}
		return validateIndexElement(entry, next, true)
	default:
		return fmt.Errorf("unsupported table constraint or index type %q", entry[start].raw)
	}
}

func validateIndexElement(entry []sqlToken, pos int, allowName bool) error {
	if allowName && pos < len(entry) && isIdentifierToken(entry[pos]) && !tokenIsKeywordAt(entry, pos, "using") && entry[pos].raw != "(" {
		pos++
	}
	if tokenIsKeywordAt(entry, pos, "using") {
		if pos+1 >= len(entry) || !isIdentifierToken(entry[pos+1]) {
			return fmt.Errorf("index USING method is truncated")
		}
		pos += 2
	}
	if tokenIsKeywordAt(entry, pos, "nulls") {
		pos++
		if tokenIsKeywordAt(entry, pos, "not") {
			pos++
		}
		if !tokenIsKeywordAt(entry, pos, "distinct") {
			return fmt.Errorf("index NULLS clause is malformed")
		}
		pos++
	}
	if pos >= len(entry) || entry[pos].raw != "(" {
		return fmt.Errorf("index column list is missing")
	}
	close, err := matchingParen(entry, pos)
	if err != nil {
		return fmt.Errorf("index column list: %w", err)
	}
	return validateIndexTail(entry, close+1)
}

func validateIndexTail(entry []sqlToken, pos int) error {
	for pos < len(entry) {
		switch {
		case tokenIsKeywordAt(entry, pos, "using"):
			if pos+1 >= len(entry) || !isIdentifierToken(entry[pos+1]) {
				return fmt.Errorf("index USING method is truncated")
			}
			pos += 2
		case tokenIsKeywordAt(entry, pos, "key_block_size"):
			pos++
			if pos < len(entry) && entry[pos].raw == "=" {
				pos++
			}
			if pos >= len(entry) || entry[pos].kind != tokenNumber {
				return fmt.Errorf("KEY_BLOCK_SIZE value is missing")
			}
			pos++
		case tokenIsKeywordAt(entry, pos, "with") && tokenIsKeywordAt(entry, pos+1, "parser"):
			if pos+2 >= len(entry) || !isIdentifierToken(entry[pos+2]) {
				return fmt.Errorf("WITH PARSER value is missing")
			}
			pos += 3
		case tokenIsKeywordAt(entry, pos, "with") && pos+1 < len(entry) && entry[pos+1].raw == "(":
			close, err := matchingParen(entry, pos+1)
			if err != nil {
				return fmt.Errorf("index WITH options: %w", err)
			}
			pos = close + 1
		case tokenIsKeywordAt(entry, pos, "include") && pos+1 < len(entry) && entry[pos+1].raw == "(":
			close, err := matchingParen(entry, pos+1)
			if err != nil {
				return fmt.Errorf("index INCLUDE columns: %w", err)
			}
			pos = close + 1
		case tokenIsKeywordAt(entry, pos, "comment"):
			if pos+1 >= len(entry) || entry[pos+1].kind != tokenString {
				return fmt.Errorf("index COMMENT value is missing")
			}
			pos += 2
		case tokenIsKeywordAt(entry, pos, "visible") || tokenIsKeywordAt(entry, pos, "invisible"):
			pos++
		default:
			return fmt.Errorf("unsupported index clause %q", entry[pos].raw)
		}
	}
	return nil
}

func validateForeignKeyElement(entry []sqlToken, pos int) error {
	if pos < len(entry) && isIdentifierToken(entry[pos]) && entry[pos].raw != "(" {
		pos++
	}
	if pos >= len(entry) || entry[pos].raw != "(" {
		return fmt.Errorf("FOREIGN KEY column list is missing")
	}
	close, err := matchingParen(entry, pos)
	if err != nil {
		return fmt.Errorf("FOREIGN KEY column list: %w", err)
	}
	pos = close + 1
	if !tokenIsKeywordAt(entry, pos, "references") {
		return fmt.Errorf("FOREIGN KEY REFERENCES clause is missing")
	}
	end, err := consumeReferencesClause(entry, pos+1)
	if err != nil {
		return err
	}
	if end != len(entry) {
		return fmt.Errorf("unsupported FOREIGN KEY clause %q", entry[end].raw)
	}
	return nil
}

func validateCheckElement(entry []sqlToken, pos int) error {
	if pos >= len(entry) || entry[pos].raw != "(" {
		return fmt.Errorf("CHECK expression is missing")
	}
	close, err := matchingParen(entry, pos)
	if err != nil {
		return fmt.Errorf("CHECK expression: %w", err)
	}
	pos = close + 1
	if tokenIsKeywordAt(entry, pos, "no") && tokenIsKeywordAt(entry, pos+1, "inherit") {
		pos += 2
	}
	if tokenIsKeywordAt(entry, pos, "not") && tokenIsKeywordAt(entry, pos+1, "enforced") {
		pos += 2
	} else if tokenIsKeywordAt(entry, pos, "enforced") {
		pos++
	}
	if pos != len(entry) {
		return fmt.Errorf("unsupported CHECK clause %q", entry[pos].raw)
	}
	return nil
}

type optionValueMode uint8

const (
	optionOne optionValueMode = iota + 1
	optionParen
	optionUntilNext
	optionFlag
	optionRest
)

type tableOptionSpec struct {
	words    []string
	name     string
	mode     optionValueMode
	foldWord bool
}

var tableOptionSpecs = []tableOptionSpec{
	{words: []string{"default", "character", "set"}, name: "charset", mode: optionOne, foldWord: true},
	{words: []string{"default", "charset"}, name: "charset", mode: optionOne, foldWord: true},
	{words: []string{"character", "set"}, name: "charset", mode: optionOne, foldWord: true},
	{words: []string{"default", "collate"}, name: "collation", mode: optionOne, foldWord: true},
	{words: []string{"data", "directory"}, name: "data-directory", mode: optionOne},
	{words: []string{"index", "directory"}, name: "index-directory", mode: optionOne},
	{words: []string{"on", "commit"}, name: "on-commit", mode: optionUntilNext, foldWord: true},
	{words: []string{"partition", "by"}, name: "partition", mode: optionRest},
	{words: []string{"without", "rowid"}, name: "without-rowid", mode: optionFlag},
	{words: []string{"auto_increment"}, name: "auto-increment", mode: optionOne},
	{words: []string{"avg_row_length"}, name: "avg-row-length", mode: optionOne},
	{words: []string{"delay_key_write"}, name: "delay-key-write", mode: optionOne, foldWord: true},
	{words: []string{"key_block_size"}, name: "key-block-size", mode: optionOne},
	{words: []string{"stats_auto_recalc"}, name: "stats-auto-recalc", mode: optionOne, foldWord: true},
	{words: []string{"stats_persistent"}, name: "stats-persistent", mode: optionOne, foldWord: true},
	{words: []string{"stats_sample_pages"}, name: "stats-sample-pages", mode: optionOne},
	{words: []string{"engine"}, name: "engine", mode: optionOne, foldWord: true},
	{words: []string{"type"}, name: "engine", mode: optionOne, foldWord: true},
	{words: []string{"charset"}, name: "charset", mode: optionOne, foldWord: true},
	{words: []string{"collate"}, name: "collation", mode: optionOne, foldWord: true},
	{words: []string{"comment"}, name: "comment", mode: optionOne},
	{words: []string{"checksum"}, name: "checksum", mode: optionOne},
	{words: []string{"max_rows"}, name: "max-rows", mode: optionOne},
	{words: []string{"min_rows"}, name: "min-rows", mode: optionOne},
	{words: []string{"pack_keys"}, name: "pack-keys", mode: optionOne, foldWord: true},
	{words: []string{"password"}, name: "password", mode: optionOne},
	{words: []string{"row_format"}, name: "row-format", mode: optionOne, foldWord: true},
	{words: []string{"insert_method"}, name: "insert-method", mode: optionOne, foldWord: true},
	{words: []string{"compression"}, name: "compression", mode: optionOne, foldWord: true},
	{words: []string{"encryption"}, name: "encryption", mode: optionOne, foldWord: true},
	{words: []string{"tablespace"}, name: "tablespace", mode: optionUntilNext},
	{words: []string{"union"}, name: "union", mode: optionParen},
	{words: []string{"with"}, name: "with", mode: optionParen},
	{words: []string{"inherits"}, name: "inherits", mode: optionParen},
	{words: []string{"using"}, name: "using", mode: optionOne, foldWord: true},
	{words: []string{"strict"}, name: "strict", mode: optionFlag},
}

func parseTableOptions(tokens []sqlToken) ([]TableOption, error) {
	options := make([]TableOption, 0, minInt(len(tokens)/2+1, MaxTableOptionCount))
	seen := make(map[string]bool)
	for pos := 0; pos < len(tokens); {
		if tokens[pos].raw == "," {
			return nil, fmt.Errorf("empty or trailing table option")
		}
		spec, ok := matchTableOption(tokens, pos)
		if !ok {
			return nil, fmt.Errorf("unsupported or truncated table option near %q", tokens[pos].raw)
		}
		if seen[spec.name] {
			return nil, fmt.Errorf("duplicate table option %q is ambiguous", spec.name)
		}
		seen[spec.name] = true
		valueStart := pos + len(spec.words)
		if valueStart < len(tokens) && tokens[valueStart].raw == "=" {
			valueStart++
		}
		valueEnd := valueStart
		switch spec.mode {
		case optionFlag:
			valueEnd = valueStart
		case optionOne:
			if valueStart >= len(tokens) || tokens[valueStart].raw == "," || tokens[valueStart].kind == tokenSymbol {
				return nil, fmt.Errorf("table option %q has no value", spec.name)
			}
			valueEnd = valueStart + 1
		case optionParen:
			if valueStart >= len(tokens) || tokens[valueStart].raw != "(" {
				return nil, fmt.Errorf("table option %q requires a parenthesized value", spec.name)
			}
			close, err := matchingParen(tokens, valueStart)
			if err != nil {
				return nil, fmt.Errorf("table option %q: %w", spec.name, err)
			}
			valueEnd = close + 1
		case optionUntilNext:
			valueEnd = nextTableOption(tokens, valueStart)
			if valueEnd == valueStart {
				return nil, fmt.Errorf("table option %q has no value", spec.name)
			}
		case optionRest:
			valueEnd = len(tokens)
			if valueEnd == valueStart {
				return nil, fmt.Errorf("table option %q has no value", spec.name)
			}
		}

		valueTokens := tokens[valueStart:valueEnd]
		if spec.name == "comment" && (len(valueTokens) != 1 || valueTokens[0].kind != tokenString) {
			return nil, fmt.Errorf("table COMMENT option requires one quoted string")
		}
		value := "true"
		canonical := "true"
		if len(valueTokens) > 0 {
			if definitionSpan(valueTokens) > MaxDefinitionBytes {
				return nil, fmt.Errorf("table option %q exceeds %d-byte definition limit", spec.name, MaxDefinitionBytes)
			}
			value = renderTokens(valueTokens)
			canonical = canonicalTokens(valueTokens, spec.foldWord)
		}
		options = append(options, TableOption{Name: spec.name, Value: value, canonical: canonical})
		if len(options) > MaxTableOptionCount {
			return nil, fmt.Errorf("table option count exceeds limit of %d", MaxTableOptionCount)
		}
		pos = valueEnd
		if pos < len(tokens) && tokens[pos].raw == "," {
			pos++
			if pos == len(tokens) {
				return nil, fmt.Errorf("trailing comma leaves an incomplete table option")
			}
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Name < options[j].Name })
	return options, nil
}

func matchTableOption(tokens []sqlToken, pos int) (tableOptionSpec, bool) {
	for _, spec := range tableOptionSpecs {
		if pos+len(spec.words) > len(tokens) {
			continue
		}
		matched := true
		for i, word := range spec.words {
			if !tokenIsKeywordAt(tokens, pos+i, word) {
				matched = false
				break
			}
		}
		if matched {
			return spec, true
		}
	}
	return tableOptionSpec{}, false
}

func nextTableOption(tokens []sqlToken, start int) int {
	depth := 0
	for pos := start; pos < len(tokens); pos++ {
		if depth == 0 {
			if tokens[pos].raw == "," {
				return pos
			}
			if pos > start {
				if _, ok := matchTableOption(tokens, pos); ok {
					return pos
				}
			}
		}
		switch tokens[pos].raw {
		case "(":
			depth++
		case ")":
			depth--
		}
	}
	return len(tokens)
}

func canonicalTokens(tokens []sqlToken, foldAllWords bool) string {
	var out strings.Builder
	for _, token := range tokens {
		marker := byte('p')
		value := token.raw
		switch token.kind {
		case tokenWord:
			lower := strings.ToLower(token.raw)
			if foldAllWords || semanticKeywords[lower] {
				marker = 'k'
				value = lower
			} else {
				marker = 'i'
				value = identifierTokenKey(token)
			}
		case tokenQuotedIdentifier:
			marker = 'i'
			value = identifierTokenKey(token)
		case tokenString:
			marker = 's'
		case tokenNumber:
			marker = 'n'
		case tokenSymbol:
			marker = 'p'
		}
		out.WriteByte(marker)
		out.WriteString(strconv.Itoa(len(value)))
		out.WriteByte(':')
		out.WriteString(value)
		out.WriteByte(';')
	}
	return out.String()
}

func renderTokens(tokens []sqlToken) string {
	var out strings.Builder
	for i, token := range tokens {
		if i > 0 && tokenNeedsSpace(tokens[i-1], token) {
			out.WriteByte(' ')
		}
		out.WriteString(token.raw)
	}
	return out.String()
}

func tokenNeedsSpace(previous, current sqlToken) bool {
	if current.raw == ")" || current.raw == "," || current.raw == ";" || current.raw == "." {
		return false
	}
	if previous.raw == "(" || previous.raw == "." {
		return false
	}
	if previous.raw == "," {
		return false
	}
	if current.raw == "(" {
		return false
	}
	return true
}

func definitionSpan(tokens []sqlToken) int {
	if len(tokens) == 0 {
		return 0
	}
	return tokens[len(tokens)-1].end - tokens[0].start
}

func isIdentifierToken(token sqlToken) bool {
	return token.kind == tokenWord || token.kind == tokenQuotedIdentifier
}

func identifierValue(token sqlToken) string {
	if token.kind == tokenQuotedIdentifier {
		return token.value
	}
	return token.raw
}

func identifierTokenKey(token sqlToken) string {
	if token.kind != tokenQuotedIdentifier {
		return "bare:" + token.raw
	}
	value := token.value
	if isPortableLowerBareIdentifier(value) && !semanticKeywords[value] {
		return "bare:" + value
	}
	quote := "?"
	if token.raw != "" {
		quote = token.raw[:1]
	}
	return "quoted:" + quote + ":" + value
}

func isPortableLowerBareIdentifier(value string) bool {
	if value == "" || strings.ToLower(value) != value || !isWordStart(value[0]) || value[0] >= 0x80 {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !isWordContinue(value[i]) || value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func tokenIsKeywordAt(tokens []sqlToken, pos int, keyword string) bool {
	return pos >= 0 && pos < len(tokens) && tokens[pos].kind == tokenWord && strings.EqualFold(tokens[pos].raw, keyword)
}

var semanticKeywords = func() map[string]bool {
	result := wordSet(`
		array as asc auto_increment autoincrement bigint binary bit blob bool boolean by
		cascade case cast char character check collate column comment compression conflict
		constraint create current_date current_time current_timestamp date datetime decimal
		default deferrable delete desc distinct double else end enum exists false first float
		foreign generated identity if in initially int integer interval key last localtime
		localtimestamp long match mediumint national no not null numeric on primary real
		references restrict set smallint spatial stored table text then time timestamp tinyint
		true unique unsigned update using uuid values varchar varying virtual when with without
		zerofill zone precision always rowid engine charset fulltext index temporary temp
		unlogged replace or partition inherits strict tablespace
	`)
	for word := range knownTypeWords {
		result[word] = true
	}
	for word := range typeContinuationWords {
		result[word] = true
	}
	for word := range columnClauseWords {
		result[word] = true
	}
	for word := range tableElementKeywords {
		result[word] = true
	}
	for _, spec := range tableOptionSpecs {
		for _, word := range spec.words {
			result[word] = true
		}
	}
	return result
}()
