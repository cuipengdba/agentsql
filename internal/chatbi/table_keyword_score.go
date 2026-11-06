package chatbi

import (
	"math"
	"strings"
	"unicode"
)

// scoreTableKeywords follows SQLBot Community v1.10.3's table keyword score
// for the input supplied to this preselection stage. The caller passes only
// currently authorized table/column metadata; remarks are descriptions, not
// instructions or a source of permission.
func scoreTableKeywords(input string, table modelTableCandidate, fieldComments map[string]string) float64 {
	var keywords []string
	for _, raw := range strings.Split(input, ",") {
		if keyword := strings.ToLower(strings.TrimSpace(raw)); keyword != "" {
			keywords = append(keywords, keyword)
		}
	}
	if len(keywords) == 0 {
		return 0
	}
	name := strings.ToLower(table.name)
	comment := strings.ToLower(table.comment)
	nameParts := keywordParts(name)
	scores := make(map[string]float64)
	for _, keyword := range keywords {
		if keyword == name {
			scores[keyword] = 1
			continue
		}
		if comment != "" && keyword == comment {
			scores[keyword] = max(scores[keyword], 0.9)
			continue
		}
		if comment != "" && strings.Contains(comment, keyword) {
			scores[keyword] = max(scores[keyword], 0.7)
			continue
		}
		if nameParts[keyword] {
			scores[keyword] = max(scores[keyword], 0)
		}
		for _, field := range table.fields {
			fieldComment := strings.ToLower(strings.TrimSpace(fieldComments[field.Name]))
			if fieldComment != "" {
				if keyword == fieldComment {
					scores[keyword] = max(scores[keyword], 0.5)
					break
				}
				if strings.Contains(fieldComment, keyword) {
					scores[keyword] = max(scores[keyword], 0.4)
				}
			}
		}
		if scores[keyword] < 0.3 {
			for _, field := range table.fields {
				if keywordParts(strings.ToLower(field.Name))[keyword] {
					scores[keyword] = max(scores[keyword], 0.3)
					break
				}
			}
		}
	}
	if len(scores) == 0 {
		return 0
	}
	if scores[name] == 1 {
		return 1
	}
	if comment != "" && scores[comment] == 0.9 {
		return 0.9
	}
	matchedParts := 0
	for _, keyword := range keywords {
		if nameParts[keyword] {
			matchedParts++
		}
	}
	if matchedParts > 0 && len(nameParts) > 0 {
		nameScore := roundKeywordScore(0.25 + 0.25*float64(matchedParts)/float64(len(nameParts)))
		for _, keyword := range keywords {
			if nameParts[keyword] {
				scores[keyword] = max(scores[keyword], nameScore)
			}
		}
	}
	best := 0.0
	for _, score := range scores {
		best = max(best, score)
	}
	return roundKeywordScore(best * float64(len(scores)) / float64(len(keywords)))
}

func keywordParts(value string) map[string]bool {
	parts := make(map[string]bool)
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '_' || unicode.IsSpace(r) }) {
		parts[part] = true
	}
	return parts
}

func roundKeywordScore(value float64) float64 {
	return math.RoundToEven(value*100) / 100
}
