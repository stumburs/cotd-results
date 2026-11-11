package main

import (
	"fmt"
	"sort"
	"strings"
)

type messageSection struct {
	Title string
	Lines []string
}

func buildMessageChunks(state *CupState, cfg *Config) []string {
	label := cfg.editionLabel(state.Edition)
	header := fmt.Sprintf("**%s** — %s", state.Name, label)

	status := "qualifier divisions"
	if state.Completed {
		status = "final results"
	} else if state.QualifierDone {
		status = "live, updating as divisions finish"
	}

	var summary string
	switch {
	case state.NoQualifier:
		summary = "No qualifier was found for this cup."
	case !state.QualifierDone:
		summary = "Waiting for the qualifier to finish…"
	case len(state.Members) == 0:
		if state.QualifierCardinal > 0 {
			summary = fmt.Sprintf("No club members qualified (out of %s players).", formatInt(state.QualifierCardinal))
		} else {
			summary = "No club members qualified."
		}
	case state.QualifierCardinal > 0:
		summary = fmt.Sprintf("%d club member%s qualified out of %s · %s",
			len(state.Members), plural(len(state.Members)), formatInt(state.QualifierCardinal), status)
	default:
		summary = fmt.Sprintf("%d club member%s qualified · %s",
			len(state.Members), plural(len(state.Members)), status)
	}
	header += "\n" + summary

	grouped := make(map[int][]MemberState)
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

		final := true
		for _, member := range members {
			if !member.DivisionFinal {
				final = false
				break
			}
		}
		stateText := "racing"
		if final {
			stateText = "final"
		}

		lines := make([]string, 0, len(members))
		for _, member := range members {
			line := fmt.Sprintf("**Q#%d** %s — %s",
				member.QualifierRank,
				sanitizeName(member.Name),
				formatQualifierScore(member.QualifierScore, state.QualifierUnit),
			)
			if member.DivisionFinal {
				if member.NoResult || member.MatchRank <= 0 {
					line += " → no result"
				} else {
					line += fmt.Sprintf(" → **#%d** (overall **#%d**)", member.MatchRank, member.OverallRank)
				}
			}
			lines = append(lines, line)
		}

		sections = append(sections, messageSection{
			Title: fmt.Sprintf("**Division %d** — %s", division, stateText),
			Lines: lines,
		})
	}

	return chunkSections(header, sections, discordContentLimit)
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
		if curLen > 0 {
			addLine("")
		}
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

func formatQualifierScore(score int64, unit string) string {
	switch unit {
	case "time":
		return formatTime(score)
	case "point", "points":
		return fmt.Sprintf("%d pts", score)
	default:
		if score == 0 {
			return "0"
		}
		return fmt.Sprintf("%d", score)
	}
}

func formatTime(ms int64) string {
	if ms < 0 {
		return "?"
	}
	hours := ms / 3_600_000
	minutes := (ms % 3_600_000) / 60_000
	seconds := (ms % 60_000) / 1_000
	millis := ms % 1_000
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", hours, minutes, seconds, millis)
	}
	return fmt.Sprintf("%d:%02d.%03d", minutes, seconds, millis)
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "`", "'")
	name = strings.TrimSpace(name)
	if name == "" {
		return "unknown"
	}
	return name
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
