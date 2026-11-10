package config

import "testing"

// The review account is opt-in and fails closed: an account without its
// password (or the reverse) must leave the feature off entirely.
func TestReviewAccountNeedsBothHalves(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOVEACED_DEV", "1")

	t.Setenv("LOVEACED_REVIEW_ACCOUNT", "20771234")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ReviewAccount != "" || c.ReviewPassword != "" {
		t.Fatalf("account without password must stay off, got account %q", c.ReviewAccount)
	}

	t.Setenv("LOVEACED_REVIEW_PASSWORD", "review-only-secret")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ReviewAccount != "20771234" || c.ReviewPassword != "review-only-secret" {
		t.Fatalf("both halves set: account %q, password set %v", c.ReviewAccount, c.ReviewPassword != "")
	}
}
