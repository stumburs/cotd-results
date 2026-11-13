package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"cotd-results/internal/config"
	"cotd-results/internal/model"
)

// Discord embed descriptions allow 4096 characters; stay conservative because
// Go counts bytes and player names can be multi-byte.
const embedDescriptionLimit = 3800

type messageSection struct {
	Title string
	Lines []string
}

func BuildMessages(state *model.CupState, cfg *config.Config) []model.OutgoingMessage {
	title := cupTitle(state, cfg)
	header := buildHeader(state, cfg)
	sections := buildSections(state)

	bodies := chunkSections(header, sections, embedDescriptionLimit)
	messages := make([]model.OutgoingMessage, len(bodies))
	for i, body := range bodies {
		messageTitle := title
		if len(bodies) > 1 {
			messageTitle = fmt.Sprintf("%s (%d/%d)", title, i+1, len(bodies))
		}
		messages[i] = model.OutgoingMessage{Title: messageTitle, Description: body}
	}
	return messages
}

func cupTitle(state *model.CupState, cfg *config.Config) string {
	start := state.StartDate
	if start <= 0 {
		start = time.Now().Unix()
	}
	title := fmt.Sprintf("%s %s #%d",
		time.Unix(start, 0).UTC().Format("2006-01-02"),
		cupTypeShort(cfg.Cups.Type),
		state.Edition,
	)
	switch {
	case state.Completed:
		title += " has finished"
	case state.QualifierDone:
		title += " - live"
	default:
		title += " - qualifiers"
	}
	return title
}

func cupTypeShort(cupType string) string {
	switch strings.ToLower(strings.TrimSpace(cupType)) {
	case "", "cotd":
		return "COTD"
	case "cotw":
		return "COTW"
	case "grand_race":
		return "GRAND RACE"
	default:
		return strings.ToUpper(strings.ReplaceAll(cupType, "_", " "))
	}
}

func buildHeader(state *model.CupState, cfg *config.Config) string {
	label := cfg.EditionLabel(state.Edition)

	var summary string
	switch {
	case state.NoQualifier:
		summary = "No qualifier was found for this cup"
	case !state.QualifierDone:
		summary = "Waiting for the qualifier to finish..."
	case len(state.Members) == 0:
		summary = "No club members qualified"
		if state.QualifierCardinal > 0 {
			summary += fmt.Sprintf(" - (%s total players)", formatInt(state.QualifierCardinal))
		}
	default:
		summary = fmt.Sprintf("%d club member%s", len(state.Members), plural(len(state.Members)))
		if state.QualifierCardinal > 0 {
			summary += fmt.Sprintf(" (%s total players)", formatInt(state.QualifierCardinal))
		}
	}

	return fmt.Sprintf("**%s** - %s", label, summary)
}

func buildSections(state *model.CupState) []messageSection {
	for _, member := range state.Members {
		if member.DivisionFinal {
			return finalSections(state)
		}
	}
	return qualifierSections(state)
}

// qualifierSections shows the pre-cup view: players grouped by division and
// ordered by their qualifier rank.
func qualifierSections(state *model.CupState) []messageSection {
	grouped := make(map[int][]model.MemberState)
	for _, member := range state.Members {
		grouped[member.Division] = append(grouped[member.Division], member)
	}
	divisions := make([]int, 0, len(grouped))
	for division := range grouped {
		divisions = append(divisions, division)
	}
	sort.Ints(divisions)

	sections := make([]messageSection, 0, len(divisions))
	for _, division := range divisions {
		members := grouped[division]
		sort.Slice(members, func(i, j int) bool {
			return members[i].QualifierRank < members[j].QualifierRank
		})

		lines := make([]string, 0, len(members))
		for _, member := range members {
			lines = append(lines, qualifierMemberLine(member))
		}

		sections = append(sections, messageSection{
			Title: fmt.Sprintf("- **Division %d**", division),
			Lines: lines,
		})
	}
	return sections
}

// finalSections is used as soon as any division has finished. Results stay
// grouped by division, but finished divisions are ordered by finishing
// position and show the relative match rank. Divisions that are still racing
// keep the qualifier-style view.
func finalSections(state *model.CupState) []messageSection {
	grouped := make(map[int][]model.MemberState)
	divisionFinal := make(map[int]bool)
	for _, member := range state.Members {
		grouped[member.Division] = append(grouped[member.Division], member)
		if member.DivisionFinal {
			divisionFinal[member.Division] = true
		}
	}

	divisions := make([]int, 0, len(grouped))
	for division := range grouped {
		divisions = append(divisions, division)
	}
	sort.Ints(divisions)

	sections := make([]messageSection, 0, len(divisions))
	for _, division := range divisions {
		members := grouped[division]
		if divisionFinal[division] {
			sort.Slice(members, func(i, j int) bool {
				a, b := members[i], members[j]
				aRanked := a.MatchRank > 0 && !a.NoResult
				bRanked := b.MatchRank > 0 && !b.NoResult
				if aRanked != bRanked {
					return aRanked
				}
				if aRanked && a.MatchRank != b.MatchRank {
					return a.MatchRank < b.MatchRank
				}
				return a.QualifierRank < b.QualifierRank
			})

			lines := make([]string, 0, len(members))
			for _, member := range members {
				lines = append(lines, finalMemberLine(member))
			}
			sections = append(sections, messageSection{
				Title: fmt.Sprintf("- **Division %d**", division),
				Lines: lines,
			})
			continue
		}

		sort.Slice(members, func(i, j int) bool {
			return members[i].QualifierRank < members[j].QualifierRank
		})
		lines := make([]string, 0, len(members))
		for _, member := range members {
			lines = append(lines, qualifierMemberLine(member))
		}
		sections = append(sections, messageSection{
			Title: fmt.Sprintf("- **Division %d** *(live)*", division),
			Lines: lines,
		})
	}
	return sections
}

func qualifierMemberLine(member model.MemberState) string {
	return fmt.Sprintf("**%d** %s", member.QualifierRank, memberDisplayName(member))
}

func finalMemberLine(member model.MemberState) string {
	name := memberDisplayName(member)
	if member.NoResult || member.MatchRank <= 0 {
		return fmt.Sprintf("*(DNF)* %s", name)
	}
	return fmt.Sprintf("**%d** %s", member.MatchRank, name)
}

func memberDisplayName(member model.MemberState) string {
	return sanitizeName(member.Name)
}

func chunkSections(header string, sections []messageSection, limit int) []string {
	var (
		chunks []string
		cur    strings.Builder
		curLen int
	)

	flush := func() {
		if curLen == 0 {
			return
		}
		chunks = append(chunks, strings.TrimRight(cur.String(), "\n"))
		cur.Reset()
		curLen = 0
	}

	addLine := func(line string) {
		extra := len(line)
		if curLen > 0 {
			extra++
		}
		if curLen+extra > limit && curLen > 0 {
			flush()
			extra = len(line)
		}
		if curLen > 0 {
			cur.WriteByte('\n')
			curLen++
		}
		cur.WriteString(line)
		curLen += len(line)
	}

	for _, line := range strings.Split(header, "\n") {
		addLine(line)
	}

	for _, section := range sections {
		if curLen+len(section.Title)+1 > limit {
			flush()
		}
		addLine(section.Title)

		continuation := 1
		for _, line := range section.Lines {
			if curLen+1+len(line) > limit {
				flush()
				continuation++
				addLine(fmt.Sprintf("%s (cont. %d)", section.Title, continuation))
			}
			addLine(line)
		}
	}

	flush()
	if len(chunks) == 0 {
		chunks = []string{header}
	}
	return chunks
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return "unknown"
	}
	return escapeMarkdown(name)
}

// escapeMarkdown prevents usernames from triggering Discord's markdown
// formatting. Backslashes are escaped first so the added escapes are not
// escaped a second time.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	`*`, `\*`,
	`_`, `\_`,
	`~`, `\~`,
	"`", "\\`",
	`|`, `\|`,
	`>`, `\>`,
	`#`, `\#`,
	`-`, `\-`,
	`[`, `\[`,
	`]`, `\]`,
)

func escapeMarkdown(name string) string {
	return markdownEscaper.Replace(name)
}

func formatInt(value int) string {
	if value < 1000 {
		return fmt.Sprintf("%d", value)
	}
	s := fmt.Sprintf("%d", value)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
