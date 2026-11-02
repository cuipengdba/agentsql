package chatbi

import (
	"sort"
	"strings"
)

type mentionSpan struct {
	start, end int
	name       string
}

func explicitTableMentions(question string, tables []modelTableCandidate) map[string]bool {
	question = strings.ToLower(question)
	quoted := quotedIdentifierSpans(question)
	names := make(map[string][]mentionSpan)
	comments := make(map[string][]mentionSpan)
	for _, table := range tables {
		name := strings.ToLower(strings.TrimSpace(table.name))
		if name != "" {
			if _, found := names[name]; !found {
				spans := identifierSpans(question, name, quoted)
				for _, quote := range quoted {
					if quote.name == name {
						spans = append(spans, mentionSpan{start: quote.start, end: quote.end})
					}
				}
				names[name] = spans
			}
		}
		comment := strings.ToLower(strings.TrimSpace(table.comment))
		if comment != "" {
			if _, found := comments[comment]; !found {
				comments[comment] = identifierSpans(question, comment, nil)
			}
		}
	}
	selectedNames := longestMentions(names)
	selectedComments := longestMentions(comments)
	result := make(map[string]bool)
	for _, table := range tables {
		if selectedNames[strings.ToLower(strings.TrimSpace(table.name))] ||
			selectedComments[strings.ToLower(strings.TrimSpace(table.comment))] {
			result[table.key] = true
		}
	}
	return result
}

func longestMentions(candidates map[string][]mentionSpan) map[string]bool {
	var spans []mentionSpan
	for name, matches := range candidates {
		for _, match := range matches {
			match.name = name
			spans = append(spans, match)
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	result := make(map[string]bool)
	furthest := -1
	for _, match := range spans {
		if match.end > furthest {
			result[match.name] = true
			furthest = match.end
		}
	}
	return result
}

func identifierSpans(question, name string, quoted []mentionSpan) []mentionSpan {
	var result []mentionSpan
	for start := 0; start < len(question); {
		offset := strings.Index(question[start:], name)
		if offset < 0 {
			break
		}
		position := start + offset
		end := position + len(name)
		before := !asciiIdentifier(name[0]) || position == 0 || !asciiIdentifier(question[position-1])
		after := !asciiIdentifier(name[len(name)-1]) || end == len(question) || !asciiIdentifier(question[end])
		insideQuote := false
		for _, quote := range quoted {
			if position < quote.end && quote.start < end {
				insideQuote = true
				break
			}
		}
		if before && after && !insideQuote {
			result = append(result, mentionSpan{start: position, end: end})
		}
		start = position + 1
	}
	return result
}

func quotedIdentifierSpans(question string) []mentionSpan {
	var result []mentionSpan
	for start := 0; start < len(question); start++ {
		opening := question[start]
		if opening != '"' && opening != '`' && opening != '[' {
			continue
		}
		closing := opening
		if opening == '[' {
			closing = ']'
		}
		var name strings.Builder
		end := start + 1
		for ; end < len(question); end++ {
			if question[end] != closing {
				name.WriteByte(question[end])
				continue
			}
			if end+1 < len(question) && question[end+1] == closing {
				name.WriteByte(closing)
				end++
				continue
			}
			result = append(result, mentionSpan{start: start, end: end + 1, name: name.String()})
			start = end
			break
		}
	}
	return result
}
