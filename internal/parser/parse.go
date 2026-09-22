package parser

import (
	"fmt"
	"os"
	"strings"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/pashagolub/pglex"
)

// Parse parses a SQL file and returns ParsedSQL with statements
func Parse(file *discovery.DiscoveredFile) (*ParsedSQL, error) {
	// Read file content
	content, err := os.ReadFile(file.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	sql := string(content)

	// Split into statements using the plpgsql scanner
	statements := splitAndClassify(sql)

	return &ParsedSQL{
		File:       file,
		Statements: statements,
	}, nil
}

// ParseStatements parses SQL text directly and returns statements
func ParseStatements(sql string) []*Statement {
	return splitAndClassify(sql)
}

// calculateLineNumber converts a byte offset to a 1-indexed line number
func calculateLineNumber(sql string, offset int) int {
	if offset < 0 {
		return 1
	}
	if offset > len(sql) {
		offset = len(sql)
	}

	lineNum := 1
	for i := 0; i < offset && i < len(sql); i++ {
		if sql[i] == '\n' {
			lineNum++
		}
	}
	return lineNum
}

// splitAndClassify splits SQL text into statements using the scanner and
// classifies each one by inspecting its leading tokens.
func splitAndClassify(sql string) []*Statement {
	tokenGroups := pglex.SplitStatements(sql)
	var statements []*Statement

	for _, toks := range tokenGroups {
		// Filter to non-comment, non-whitespace tokens for classification,
		// but keep all tokens to compute raw SQL span.
		var significant []pglex.Token
		for _, t := range toks {
			if t.Type != pglex.Comment {
				significant = append(significant, t)
			}
		}
		if len(significant) == 0 {
			continue // skip comment-only groups
		}

		// Compute raw SQL from first token position to last token end.
		firstPos := toks[0].Pos
		lastTok := toks[len(toks)-1]
		rawSQL := sql[firstPos : lastTok.Pos+len(lastTok.Text)]

		startLine := calculateLineNumber(sql, firstPos)
		endLine := calculateLineNumber(sql, lastTok.Pos+len(lastTok.Text))

		stmt := &Statement{
			RawSQL:    rawSQL,
			StartPos:  firstPos,
			StartLine: startLine,
			EndLine:   endLine,
			Type:      classifyTokens(significant),
		}

		// For functions/procedures and DO blocks, extract body and language.
		switch stmt.Type {
		case StmtFunction, StmtProcedure:
			stmt.Language = extractLanguage(significant)
			stmt.Body, stmt.BodyStart = extractBody(significant, firstPos)
		case StmtDO:
			stmt.Language = extractDOLanguage(significant)
			if stmt.Language == "" {
				stmt.Language = "plpgsql" // DO blocks default to plpgsql
			}
			stmt.Body, stmt.BodyStart = extractDOBody(significant, firstPos)
		}

		statements = append(statements, stmt)
	}

	return statements
}

// classifyTokens determines the statement type from its leading tokens.
// It scans for CREATE [OR REPLACE] FUNCTION/PROCEDURE/TRIGGER/VIEW patterns
// and DO blocks.
func classifyTokens(tokens []pglex.Token) StatementType {
	if len(tokens) == 0 {
		return StmtUnknown
	}

	// Check for DO block: first significant token is "DO" (case-insensitive)
	if isIdent(tokens[0], "DO") {
		return StmtDO
	}

	// Check for CREATE [OR REPLACE] FUNCTION/PROCEDURE/TRIGGER/VIEW
	if !isIdent(tokens[0], "CREATE") {
		return StmtOther
	}

	// Skip past CREATE [OR REPLACE]
	i := 1
	if i < len(tokens) && isIdent(tokens[i], "OR") {
		i++
		if i < len(tokens) && isIdent(tokens[i], "REPLACE") {
			i++
		}
	}

	if i >= len(tokens) {
		return StmtOther
	}

	switch {
	case isIdent(tokens[i], "FUNCTION"):
		return StmtFunction
	case isIdent(tokens[i], "PROCEDURE"):
		return StmtProcedure
	case isIdent(tokens[i], "TRIGGER"):
		return StmtTrigger
	case isIdent(tokens[i], "VIEW"):
		return StmtView
	default:
		return StmtOther
	}
}

// isIdent checks whether a token is an identifier matching the given word
// (case-insensitive). Both Ident tokens and keyword tokens can match.
func isIdent(tok pglex.Token, word string) bool {
	return strings.EqualFold(tok.Text, word)
}

// extractLanguage finds the LANGUAGE clause in a CREATE FUNCTION/PROCEDURE statement.
func extractLanguage(tokens []pglex.Token) string {
	for i := 0; i < len(tokens)-1; i++ {
		if isIdent(tokens[i], "LANGUAGE") {
			return languageName(tokens[i+1])
		}
	}
	return ""
}

// extractDOLanguage finds the LANGUAGE clause in a DO block.
// DO blocks can have LANGUAGE before or after the body string.
func extractDOLanguage(tokens []pglex.Token) string {
	for i := 0; i < len(tokens)-1; i++ {
		if isIdent(tokens[i], "LANGUAGE") {
			return languageName(tokens[i+1])
		}
	}
	return ""
}

// extractBody finds the AS clause's string literal in a CREATE FUNCTION/PROCEDURE
// and returns (bodyContent, bodyOffsetInRawSQL).
func extractBody(tokens []pglex.Token, stmtStartPos int) (string, int) {
	for i := 0; i < len(tokens)-1; i++ {
		if isIdent(tokens[i], "AS") {
			next := tokens[i+1]
			if next.Type == pglex.SConst {
				body := unquoteString(next.Text)
				// bodyStart is the offset within rawSQL where the body content begins
				bodyOffset := next.Pos - stmtStartPos + bodyDelimiterLen(next.Text)
				return body, bodyOffset
			}
		}
	}
	return "", 0
}

// extractDOBody finds the body string in a DO block.
// DO $$ body $$ or DO 'body'
func extractDOBody(tokens []pglex.Token, stmtStartPos int) (string, int) {
	for i := range tokens {
		if isIdent(tokens[i], "DO") {
			// The body is the next SConst after DO (could be DO $$ ... $$ or DO 'body')
			for j := i + 1; j < len(tokens); j++ {
				if tokens[j].Type == pglex.SConst {
					body := unquoteString(tokens[j].Text)
					bodyOffset := tokens[j].Pos - stmtStartPos + bodyDelimiterLen(tokens[j].Text)
					return body, bodyOffset
				}
			}
		}
	}
	return "", 0
}

// unquoteString strips the outer quoting from a string constant token.
// Handles: $$...$$, $tag$...$tag$, '...', E'...'
func unquoteString(tokenText string) string {
	// Dollar quoting: $tag$...$tag$ or $$...$$
	if strings.HasPrefix(tokenText, "$") {
		// Find the end of the opening delimiter
		endDelim := strings.Index(tokenText[1:], "$")
		if endDelim >= 0 {
			delimLen := endDelim + 2 // length including both $
			delim := tokenText[:delimLen]
			// Body is between opening and closing delimiter
			inner := tokenText[delimLen:]
			if strings.HasSuffix(inner, delim) {
				return inner[:len(inner)-delimLen]
			}
		}
		return tokenText // fallback
	}

	// E'...' escape strings
	if len(tokenText) >= 3 && (tokenText[0] == 'E' || tokenText[0] == 'e') && tokenText[1] == '\'' {
		return tokenText[2 : len(tokenText)-1]
	}

	// Plain '...' strings
	if len(tokenText) >= 2 && tokenText[0] == '\'' && tokenText[len(tokenText)-1] == '\'' {
		return tokenText[1 : len(tokenText)-1]
	}

	return tokenText
}

// bodyDelimiterLen returns the length of the opening delimiter of a string constant.
func bodyDelimiterLen(tokenText string) int {
	// Dollar quoting: $tag$
	if strings.HasPrefix(tokenText, "$") {
		endDelim := strings.Index(tokenText[1:], "$")
		if endDelim >= 0 {
			return endDelim + 2
		}
	}
	// E'
	if len(tokenText) >= 2 && (tokenText[0] == 'E' || tokenText[0] == 'e') && tokenText[1] == '\'' {
		return 2
	}
	// '
	if len(tokenText) >= 1 && tokenText[0] == '\'' {
		return 1
	}
	return 0
}

// languageName lowercases a LANGUAGE token, accepting the quoted spelling
// (LANGUAGE 'plpgsql') that PostgreSQL still allows.
func languageName(tok pglex.Token) string {
	return strings.ToLower(strings.Trim(tok.Text, `'"`))
}
