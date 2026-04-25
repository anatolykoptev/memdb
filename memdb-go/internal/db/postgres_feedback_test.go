package db

// postgres_feedback_test.go — unit tests for postgres_feedback.go.
// These tests exercise validation logic without a live database.

import (
	"strings"
	"testing"
)

// --- validateFeedbackEventParams ---

func TestValidateFeedbackEventParams_Valid(t *testing.T) {
	cases := []InsertFeedbackEventParams{
		{UserID: "u1", Query: "what is my name?", Prediction: "Alex", Label: "positive"},
		{UserID: "u2", Query: "q", Prediction: "p", Label: "negative"},
		{UserID: "u3", Query: "q", Prediction: "p", Label: "neutral"},
		{UserID: "u4", Query: "q", Prediction: "p", Label: "correction"},
	}
	for _, c := range cases {
		if err := validateFeedbackEventParams(c); err != nil {
			t.Errorf("validateFeedbackEventParams(%+v) unexpected error: %v", c, err)
		}
	}
}

func TestValidateFeedbackEventParams_MissingUserID(t *testing.T) {
	err := validateFeedbackEventParams(InsertFeedbackEventParams{
		Query: "q", Prediction: "p", Label: "positive",
	})
	if err == nil {
		t.Fatal("expected error for empty user_id")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Errorf("error should mention user_id, got: %v", err)
	}
}

func TestValidateFeedbackEventParams_MissingQuery(t *testing.T) {
	err := validateFeedbackEventParams(InsertFeedbackEventParams{
		UserID: "u", Prediction: "p", Label: "positive",
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query") {
		t.Errorf("error should mention query, got: %v", err)
	}
}

func TestValidateFeedbackEventParams_MissingPrediction(t *testing.T) {
	err := validateFeedbackEventParams(InsertFeedbackEventParams{
		UserID: "u", Query: "q", Label: "positive",
	})
	if err == nil {
		t.Fatal("expected error for empty prediction")
	}
	if !strings.Contains(err.Error(), "prediction") {
		t.Errorf("error should mention prediction, got: %v", err)
	}
}

func TestValidateFeedbackEventParams_InvalidLabel(t *testing.T) {
	err := validateFeedbackEventParams(InsertFeedbackEventParams{
		UserID: "u", Query: "q", Prediction: "p", Label: "unknown",
	})
	if err == nil {
		t.Fatal("expected error for invalid label")
	}
	if !strings.Contains(err.Error(), "label") {
		t.Errorf("error should mention label, got: %v", err)
	}
}

// --- stub validation paths (no live DB required) ---

func TestInsertFeedbackEvent_ValidationErrors(t *testing.T) {
	p := NewStubPostgres()

	cases := []InsertFeedbackEventParams{
		{Query: "q", Prediction: "p", Label: "positive"},               // missing UserID
		{UserID: "u", Prediction: "p", Label: "positive"},              // missing Query
		{UserID: "u", Query: "q", Label: "positive"},                   // missing Prediction
		{UserID: "u", Query: "q", Prediction: "p", Label: "bad_label"}, // invalid Label
	}
	for _, params := range cases {
		_, err := p.InsertFeedbackEvent(nil, params) //nolint:staticcheck // nil ctx triggers validation-only path
		if err == nil {
			t.Errorf("InsertFeedbackEvent(%+v) expected validation error, got nil", params)
		}
	}
}

func TestGetFeedbackEventsByUser_EmptyUserID(t *testing.T) {
	p := NewStubPostgres()
	_, err := p.GetFeedbackEventsByUser(nil, "") //nolint:staticcheck // nil ctx triggers validation-only path
	if err == nil {
		t.Fatal("expected error for empty user_id")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Errorf("error should mention user_id, got: %v", err)
	}
}

func TestInsertExtractExample_ValidationErrors(t *testing.T) {
	p := NewStubPostgres()

	cases := []InsertExtractExampleParams{
		{InputText: "x", GoldOutput: []byte(`{}`)},          // missing PromptKind
		{PromptKind: "profile_extract", InputText: "x"},     // missing GoldOutput
	}
	for _, params := range cases {
		_, err := p.InsertExtractExample(nil, params) //nolint:staticcheck // nil ctx triggers validation-only path
		if err == nil {
			t.Errorf("InsertExtractExample(%+v) expected validation error, got nil", params)
		}
	}
}
