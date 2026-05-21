package scoring

import (
	"math"
	"strconv"
)

// ScoreRanged determines which teams win a ranged question.
// correctAnswer is the string representation of the correct numeric value.
// teamAnswers maps team_id to their submitted answer string.
// Returns a map of team_id -> bool (true = winner).
func ScoreRanged(correctAnswer string, teamAnswers map[int64]string) map[int64]bool {
	results := make(map[int64]bool)

	correct, err := strconv.ParseFloat(correctAnswer, 64)
	if err != nil {
		// If correct answer isn't numeric, nobody wins
		for id := range teamAnswers {
			results[id] = false
		}
		return results
	}

	if len(teamAnswers) == 0 {
		return results
	}

	// Find minimum absolute difference
	minDiff := math.MaxFloat64
	teamDiffs := make(map[int64]float64)

	for teamID, answer := range teamAnswers {
		val, err := strconv.ParseFloat(answer, 64)
		if err != nil {
			results[teamID] = false
			teamDiffs[teamID] = math.MaxFloat64
			continue
		}
		diff := math.Abs(val - correct)
		teamDiffs[teamID] = diff
		if diff < minDiff {
			minDiff = diff
		}
	}

	// All teams with the minimum difference win (tie rule)
	for teamID, diff := range teamDiffs {
		results[teamID] = diff == minDiff && diff != math.MaxFloat64
	}

	return results
}

// ScoreMultipleChoice determines which teams answered correctly.
// correctAnswer is the correct option letter (e.g., "A", "B", "C", "D").
// teamAnswers maps team_id to their submitted answer string.
// Returns a map of team_id -> bool (true = correct).
func ScoreMultipleChoice(correctAnswer string, teamAnswers map[int64]string) map[int64]bool {
	results := make(map[int64]bool)

	for teamID, answer := range teamAnswers {
		results[teamID] = answer == correctAnswer
	}

	return results
}