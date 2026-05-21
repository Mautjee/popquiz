package scoring_test

import (
	"testing"

	"github.com/mundi/popquiz/internal/scoring"
)

func TestScoreRanged_ClosestWins(t *testing.T) {
	answers := map[int64]string{1: "95", 2: "98", 3: "110"}
	results := scoring.ScoreRanged("100", answers)

	if results[1] != false {
		t.Error("Team 1 (95, diff=5) should not win")
	}
	if results[2] != true {
		t.Error("Team 2 (98, diff=2) should win")
	}
	if results[3] != false {
		t.Error("Team 3 (110, diff=10) should not win")
	}
}

func TestScoreRanged_TieWins(t *testing.T) {
	answers := map[int64]string{1: "98", 2: "98", 3: "95"}
	results := scoring.ScoreRanged("100", answers)

	if results[1] != true {
		t.Error("Team 1 (98, diff=2) should win (tie)")
	}
	if results[2] != true {
		t.Error("Team 2 (98, diff=2) should win (tie)")
	}
	if results[3] != false {
		t.Error("Team 3 (95, diff=5) should not win")
	}
}

func TestScoreRanged_ExactMatch(t *testing.T) {
	answers := map[int64]string{1: "100", 2: "99"}
	results := scoring.ScoreRanged("100", answers)

	if results[1] != true {
		t.Error("Team 1 (exact match) should win")
	}
	if results[2] != false {
		t.Error("Team 2 (99, diff=1) should not win")
	}
}

func TestScoreRanged_NegativeValues(t *testing.T) {
	answers := map[int64]string{1: "-50", 2: "-48"}
	results := scoring.ScoreRanged("-50", answers)

	if results[1] != true {
		t.Error("Team 1 (exact match) should win")
	}
	if results[2] != false {
		t.Error("Team 2 (-48, diff=2) should not win")
	}
}

func TestScoreRanged_DecimalValues(t *testing.T) {
	answers := map[int64]string{1: "98.5", 2: "99.1"}
	results := scoring.ScoreRanged("98.5", answers)

	if results[1] != true {
		t.Error("Team 1 (exact match) should win")
	}
	if results[2] != false {
		t.Error("Team 2 (99.1, diff=0.6) should not win")
	}
}

func TestScoreRanged_NonNumericAnswer(t *testing.T) {
	answers := map[int64]string{1: "notanumber", 2: "100"}
	results := scoring.ScoreRanged("100", answers)

	if results[1] != false {
		t.Error("Team 1 (non-numeric) should not win")
	}
	if results[2] != true {
		t.Error("Team 2 (exact match) should win")
	}
}

func TestScoreRanged_InvalidCorrectAnswer(t *testing.T) {
	answers := map[int64]string{1: "100"}
	results := scoring.ScoreRanged("notanumber", answers)

	if results[1] != false {
		t.Error("With invalid correct answer, nobody should win")
	}
}

func TestScoreRanged_EmptyAnswers(t *testing.T) {
	answers := map[int64]string{}
	results := scoring.ScoreRanged("100", answers)

	if len(results) != 0 {
		t.Error("Empty answers should produce empty results")
	}
}

func TestScoreMultipleChoice_CorrectAnswer(t *testing.T) {
	answers := map[int64]string{1: "B", 2: "A", 3: "B"}
	results := scoring.ScoreMultipleChoice("B", answers)

	if results[1] != true {
		t.Error("Team 1 (B) should be correct")
	}
	if results[2] != false {
		t.Error("Team 2 (A) should be incorrect")
	}
	if results[3] != true {
		t.Error("Team 3 (B) should be correct")
	}
}

func TestScoreMultipleChoice_NoCorrectAnswers(t *testing.T) {
	answers := map[int64]string{1: "A", 2: "C"}
	results := scoring.ScoreMultipleChoice("B", answers)

	if results[1] != false {
		t.Error("Team 1 (A) should be incorrect")
	}
	if results[2] != false {
		t.Error("Team 2 (C) should be incorrect")
	}
}

func TestScoreMultipleChoice_AllCorrect(t *testing.T) {
	answers := map[int64]string{1: "A", 2: "A", 3: "A"}
	results := scoring.ScoreMultipleChoice("A", answers)

	for id, correct := range results {
		if !correct {
			t.Errorf("Team %d should be correct", id)
		}
	}
}

func TestScoreMultipleChoice_EmptyAnswers(t *testing.T) {
	answers := map[int64]string{}
	results := scoring.ScoreMultipleChoice("A", answers)

	if len(results) != 0 {
		t.Error("Empty answers should produce empty results")
	}
}