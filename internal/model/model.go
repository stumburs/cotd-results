// Package model holds the data shared between the tracker, storage, and
// Discord rendering.
package model

// CupState is the persisted progress of one tracked cup.
type CupState struct {
	CompetitionID int64  `json:"competitionId"`
	CupID         int64  `json:"cupId"`
	Edition       int    `json:"edition"`
	Name          string `json:"name"`
	Partition     string `json:"partition"`

	StartDate        int64 `json:"startDate"`
	EndDate          int64 `json:"endDate"`
	ChallengeStart   int64 `json:"challengeStart"`
	ChallengeEnd     int64 `json:"challengeEnd"`
	ChallengeID      int64 `json:"challengeId"`
	QualifierRoundID int64 `json:"qualifierRoundId"`
	MatchSize        int   `json:"matchSize"`

	QualifierDone      bool   `json:"qualifierDone"`
	QualifierFetchedAt int64  `json:"qualifierFetchedAt"`
	QualifierUnit      string `json:"qualifierUnit"`
	QualifierCardinal  int    `json:"qualifierCardinal"`
	NoQualifier        bool   `json:"noQualifier,omitempty"`

	Members []MemberState `json:"members"`

	Messages  []string `json:"messages"`
	WebhookID string   `json:"webhookId"`

	Completed    bool  `json:"completed"`
	HistorySaved bool  `json:"historySaved"`
	Finished     bool  `json:"finished"`
	CreatedAt    int64 `json:"createdAt"`
	UpdatedAt    int64 `json:"updatedAt"`
}

type MemberState struct {
	AccountID      string `json:"accountId"`
	Name           string `json:"name"`
	QualifierRank  int    `json:"qualifierRank"`
	QualifierScore int64  `json:"qualifierScore"`
	Division       int    `json:"division"`

	MatchID       int64 `json:"matchId,omitempty"`
	MatchRank     int   `json:"matchRank,omitempty"`
	OverallRank   int   `json:"overallRank,omitempty"`
	DivisionFinal bool  `json:"divisionFinal"`
	NoResult      bool  `json:"noResult,omitempty"`
}

// OutgoingMessage is one Discord message: a single embed with a title and
// description. Keeping the title separate lets Discord render it as an embed
// heading rather than plain message text.
type OutgoingMessage struct {
	Title       string
	Description string
}

// DivisionForRank maps a qualifier rank to a 1-based division.
func DivisionForRank(rank, matchSize int) int {
	if rank <= 0 {
		return 0
	}
	if matchSize <= 0 {
		matchSize = 64
	}
	return (rank-1)/matchSize + 1
}
