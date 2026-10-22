package core

import (
	"strings"
	"unicode"
)

func normalizedTreeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func recordText(record *ElementRecord) string {
	if record == nil {
		return ""
	}
	if text := normalizedTreeText(record.Name); text != "" {
		return text
	}
	return normalizedTreeText(record.Value)
}

func comparableTreeText(value string) string {
	return strings.ToLower(normalizedTreeText(value))
}

func containsTreeTextFragment(container, fragment string) bool {
	containerRunes := []rune(comparableTreeText(container))
	fragmentRunes := []rune(comparableTreeText(fragment))
	if len(fragmentRunes) == 0 || len(fragmentRunes) > len(containerRunes) {
		return false
	}
	isWord := func(char rune) bool {
		return unicode.IsLetter(char) || unicode.IsDigit(char)
	}
	for start := 0; start+len(fragmentRunes) <= len(containerRunes); start++ {
		matched := true
		for offset := range fragmentRunes {
			if containerRunes[start+offset] != fragmentRunes[offset] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		end := start + len(fragmentRunes)
		leftOK := start == 0 ||
			!isWord(containerRunes[start-1]) ||
			!isWord(fragmentRunes[0])
		rightOK := end == len(containerRunes) ||
			!isWord(containerRunes[end]) ||
			!isWord(fragmentRunes[len(fragmentRunes)-1])
		if leftOK && rightOK {
			return true
		}
	}
	return false
}

func populateWorkingTreeFacts(node *workingTreeNode) string {
	if node == nil {
		return ""
	}
	childTexts := []string{}
	for _, child := range node.children {
		if text := populateWorkingTreeFacts(child); text != "" {
			childTexts = append(childTexts, text)
		}
	}
	if len(childTexts) > 0 {
		node.descendantLeafText = strings.Join(childTexts, " ")
	} else {
		node.descendantLeafText = recordText(node.record)
	}
	if node.record != nil &&
		workingStructuralRoles[canonicalRole(node.record)] &&
		strings.TrimSpace(node.record.Name) == "" &&
		strings.TrimSpace(node.record.Value) != "" &&
		strings.TrimSpace(node.record.Description) == "" &&
		strings.TrimSpace(node.record.Placeholder) == "" &&
		!meaningfulWorkingStates(node.record) &&
		!meaningfulRecordActions(node.record) &&
		len(childTexts) > 0 {
		node.structuralValueRedundant =
			comparableTreeText(node.record.Value) ==
				comparableTreeText(strings.Join(childTexts, " "))
	}
	return node.descendantLeafText
}

func structuralValueIsRedundant(node *workingTreeNode) bool {
	return node != nil && node.structuralValueRedundant
}

func textContributionIsRedundant(node *workingTreeNode) bool {
	if node == nil || node.record == nil {
		return false
	}
	role := canonicalRole(node.record)
	if !workingTextRoles[role] ||
		meaningfulWorkingStates(node.record) ||
		meaningfulRecordActions(node.record) ||
		strings.TrimSpace(node.record.Description) != "" ||
		strings.TrimSpace(node.record.Placeholder) != "" {
		return false
	}
	text := recordText(node.record)
	if text == "" {
		return false
	}
	for parent := node.parent; parent != nil; parent = parent.parent {
		if structuralValueIsRedundant(parent) {
			continue
		}
		parentText := recordText(parent.record)
		if parentText == "" {
			continue
		}
		if parentText == text || containsTreeTextFragment(parentText, text) {
			return true
		}
	}
	return false
}

func keepInWorkingTree(node *workingTreeNode) bool {
	if node == nil || node.record == nil {
		return true
	}
	if node.parent == nil || len(node.notes) > 0 {
		return true
	}
	if node.forceKeep {
		return true
	}
	if textContributionIsRedundant(node) {
		return false
	}
	role := canonicalRole(node.record)
	if workingSemanticBoundary(role) {
		return true
	}
	if workingStructuralRoles[role] &&
		structuralValueIsRedundant(node) {
		return false
	}
	return recordHasWorkingSignal(node.record)
}

func rewriteTreeMetadata(
	line string,
	rewrite func(string) (string, bool),
) string {
	var out strings.Builder
	out.Grow(len(line))
	start := 0
	inQuote := false
	escaped := false
	for index := 0; index < len(line); index++ {
		switch {
		case escaped:
			escaped = false
		case line[index] == '\\' && inQuote:
			escaped = true
		case line[index] == '"':
			inQuote = !inQuote
		case line[index] == '[' && !inQuote:
			segmentEnd := index + 1
			segmentQuoted := false
			segmentEscaped := false
		segmentLoop:
			for ; segmentEnd < len(line); segmentEnd++ {
				switch {
				case segmentEscaped:
					segmentEscaped = false
				case line[segmentEnd] == '\\' && segmentQuoted:
					segmentEscaped = true
				case line[segmentEnd] == '"':
					segmentQuoted = !segmentQuoted
				case line[segmentEnd] == ']' && !segmentQuoted:
					content := line[index+1 : segmentEnd]
					replacement, changed := rewrite(content)
					if !changed {
						index = segmentEnd
						break segmentLoop
					}
					prefixEnd := index
					if replacement == "" &&
						prefixEnd > start &&
						line[prefixEnd-1] == ' ' {
						prefixEnd--
					}
					out.WriteString(line[start:prefixEnd])
					out.WriteString(replacement)
					start = segmentEnd + 1
					index = segmentEnd
					break segmentLoop
				}
			}
		}
	}
	if start == 0 {
		return line
	}
	out.WriteString(line[start:])
	return out.String()
}

func cleanWorkingTreeLine(line string, record *ElementRecord) string {
	line = normalizeWorkingQuotedWhitespace(line)
	line = rewriteTreeMetadata(
		line,
		func(content string) (string, bool) {
			if !strings.HasPrefix(content, "actions=") {
				return "", false
			}
			kept := []string{}
			for _, action := range strings.Split(
				strings.TrimPrefix(content, "actions="),
				",",
			) {
				normalized := normalizedActionName(action)
				if normalized != "clickancestor" &&
					normalized != "showcontextmenu" {
					kept = append(kept, action)
				}
			}
			if len(kept) == 0 {
				return "", true
			}
			return "[a=" + strings.Join(kept, ",") + "]", true
		},
	)
	line = stripWorkingTableCoordinates(line, record)
	if record == nil {
		return line
	}
	showClickCapability :=
		roleHasMeaningfulClickCapability(canonicalRole(record))
	return rewriteTreeMetadata(
		line,
		func(content string) (string, bool) {
			if strings.Contains(content, "=") {
				return "", false
			}
			states := strings.Fields(content)
			found := false
			kept := []string{}
			for _, state := range states {
				if state == "has-click-action" {
					found = true
					if showClickCapability {
						kept = append(kept, "click")
					}
					continue
				}
				kept = append(kept, state)
			}
			if !found {
				return "", false
			}
			if len(kept) == 0 {
				return "", true
			}
			return "[" + strings.Join(kept, " ") + "]", true
		},
	)
}

func normalizeWorkingQuotedWhitespace(line string) string {
	if !strings.ContainsAny(line, "\\\r\n\t") {
		return line
	}
	var out strings.Builder
	var quoted strings.Builder
	inQuote := false
	for index := 0; index < len(line); index++ {
		current := line[index]
		if !inQuote {
			out.WriteByte(current)
			if current == '"' {
				inQuote = true
				quoted.Reset()
			}
			continue
		}
		if current == '\\' && index+1 < len(line) {
			next := line[index+1]
			if next == 'n' || next == 'r' || next == 't' {
				quoted.WriteByte(' ')
				index++
				continue
			}
			quoted.WriteByte(current)
			quoted.WriteByte(next)
			index++
			continue
		}
		if current != '"' {
			quoted.WriteByte(current)
			continue
		}
		out.WriteString(strings.Join(strings.Fields(quoted.String()), " "))
		out.WriteByte(current)
		inQuote = false
	}
	if inQuote {
		out.WriteString(quoted.String())
	}
	return out.String()
}

func normalizedSemanticWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func equalSemanticIdentity(left, right string) bool {
	return normalizedSemanticWhitespace(left) ==
		normalizedSemanticWhitespace(right)
}

func stripWorkingTableCoordinates(line string, record *ElementRecord) string {
	if record == nil ||
		!strings.Contains(canonicalRole(record), "table cell") {
		return line
	}
	inQuote := false
	escaped := false
	for start := 0; start+4 < len(line); start++ {
		switch {
		case escaped:
			escaped = false
			continue
		case line[start] == '\\' && inQuote:
			escaped = true
			continue
		case line[start] == '"':
			inQuote = !inQuote
			continue
		case inQuote || !strings.HasPrefix(line[start:], " (R"):
			continue
		}
		endOffset := strings.IndexByte(line[start:], ')')
		if endOffset < 0 {
			return line
		}
		end := start + endOffset
		token := line[start+2 : end]
		if !validTableCoordinateToken(token) {
			continue
		}
		return line[:start] + line[end+1:]
	}
	return line
}

func validTableCoordinateToken(token string) bool {
	if len(token) < 4 || token[0] != 'R' {
		return false
	}
	index := 1
	for index < len(token) && token[index] >= '0' && token[index] <= '9' {
		index++
	}
	if index == 1 || index >= len(token) || token[index] != 'C' {
		return false
	}
	index++
	startColumn := index
	for index < len(token) && token[index] >= '0' && token[index] <= '9' {
		index++
	}
	return index > startColumn && index == len(token)
}
