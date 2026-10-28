package gate

import "testing"

func TestValidatePasswordFormat(t *testing.T) {
	cases := []struct {
		name   string
		pw     string
		wantOK bool
		want   []Category
	}{
		{"ascii ok", "Passw0rd!123", true, nil},
		{"empty", "", false, []Category{CatEmpty}},
		{"fullwidth digits", "abc１２３", false, []Category{CatFullwidthAlnum, CatFullwidthAlnum, CatFullwidthAlnum}},
		{"fullwidth symbol", "abc！def", false, []Category{CatFullwidthSymbol}},
		{"fullwidth letters", "abcａｂｃ", false, []Category{CatFullwidthAlnum, CatFullwidthAlnum, CatFullwidthAlnum}},
		{"chinese char", "密码abc", false, []Category{CatNonASCII, CatNonASCII}},
		{"leading space", " abc", false, []Category{CatWhitespaceEdge}},
		{"trailing space", "abc ", false, []Category{CatWhitespaceEdge}},
		{"inner space ok", "ab c", true, nil},
		{"newline", "ab\nc", false, []Category{CatControl}},
		{"zero width", "ab\u200bc", false, []Category{CatZeroWidth}},
		{"ideographic space", "ab\u3000c", false, []Category{CatFullwidthSymbol}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidatePasswordFormat(tc.pw, MaxPasswordLength)
			if got.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (violations %v)", got.OK, tc.wantOK, got.Violations)
			}
			if tc.wantOK {
				return
			}
			if len(got.Violations) != len(tc.want) {
				t.Fatalf("violations = %v, want categories %v", got.Violations, tc.want)
			}
			for i, want := range tc.want {
				if got.Violations[i].Category != want {
					t.Fatalf("violation[%d] = %s, want %s", i, got.Violations[i].Category, want)
				}
			}
		})
	}
}

func TestValidatePasswordTooLong(t *testing.T) {
	got := ValidatePasswordFormat("aaaaaaaaaaaaaaaa", 4) // 16 a's
	if got.OK {
		t.Fatal("too-long password accepted")
	}
	if got.Violations[len(got.Violations)-1].Category != CatTooLong {
		t.Fatalf("want too_long, got %v", got.Violations)
	}
}

func TestMaskedAccount(t *testing.T) {
	cases := map[string]string{
		"20000003": "20***03",
		"123":      "1***3",
		"12":       "***",
	}
	for in, want := range cases {
		if got := MaskedAccount(in); got != want {
			t.Fatalf("MaskedAccount(%q) = %q, want %q", in, got, want)
		}
	}
}
