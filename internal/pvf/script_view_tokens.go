package pvf

import (
	"encoding/binary"
	"math"
	"strconv"
)

// ScriptViewOf builds the same semantic projection as
// ParseScriptView(Text(index)) but reads the archive's own token stream
// directly. The text route costs a full decompile and then re-lexes the result
// just to recover what the tokens already said; this route decodes one pass and
// resolves only the string tokens.
//
// Offsets are left at zero: they describe positions in the decompiled text,
// which the token route never materialises. Callers that need them (editor
// features) must keep using Text() + ParseScriptView.
func (a *Archive) ScriptViewOf(index int32) (ScriptView, error) {
	raw, err := a.RawBytes(index)
	if err != nil {
		return ScriptView{}, err
	}
	if a.act60 != nil || index < 0 || int(index) >= len(a.items) || a.items[index].typ != TypeScript {
		text, err := a.Text(index)
		if err != nil {
			return ScriptView{}, err
		}
		return ParseScriptView(text), nil
	}
	return ScriptView{Elements: a.elementsFromTokens(raw)}, nil
}

// elementsFromTokens mirrors lexScriptText's classification token by token, so
// the two routes cannot drift in what they report as a section, a string or a
// number. String symmetry is exact by construction: decodeScript writes
// escaped content between backticks and the text lexer unescapes it, so the
// value here is the resolved string itself.
func (a *Archive) elementsFromTokens(raw []byte) []ScriptElement {
	lexemes := make([]scriptLexeme, 0, len(raw)/5)
	for i := 0; i+5 <= len(raw); i += 5 {
		typ := raw[i]
		v := int32(binary.LittleEndian.Uint32(raw[i+1:]))
		switch typ {
		case 0:
			tokenType, value := normalizeTextToken(strconv.FormatInt(int64(v), 10))
			lexemes = append(lexemes, scriptLexeme{tokenType: tokenType, value: value})
		case 2:
			tokenType, value := normalizeTextToken(formatScriptFloat(math.Float32frombits(uint32(v))))
			lexemes = append(lexemes, scriptLexeme{tokenType: tokenType, value: value})
		case 3:
			resolved := a.ResolveString(v)
			if name, closing, ok := parseSectionTag(resolved); ok {
				lexemes = append(lexemes, scriptLexeme{isSection: true, closing: closing, name: name, tokenType: 3})
				continue
			}
			tokenType, value := normalizeTextToken(resolved)
			lexemes = append(lexemes, scriptLexeme{tokenType: tokenType, value: value})
		case 5, 7, 8, 10:
			lexemes = append(lexemes, scriptLexeme{tokenType: int32(typ), value: a.ResolveString(v)})
		case 6:
			lexemes = append(lexemes, scriptLexeme{tokenType: 6, value: a.ResolveString(v)})
		default:
			// decodeScript ignores unknown kinds, so the text route never sees
			// them; skipping keeps the two projections identical.
		}
	}
	return elementsFromLexemes(lexemes, nil)
}
