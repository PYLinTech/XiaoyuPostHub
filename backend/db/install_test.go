package db

import "testing"

func TestQuoteInstallLiteral(t *testing.T) {
	if got := quoteInstallLiteral("a'b"); got != "'a''b'" {
		t.Fatalf("quoteInstallLiteral = %q", got)
	}
}

func TestInstallIdentifierPattern(t *testing.T) {
	for _, value := range []string{"xiaoyuposthub", "xph_user", "a12"} {
		if !installIdentifierPattern.MatchString(value) {
			t.Errorf("应接受 %q", value)
		}
	}
	for _, value := range []string{"ab", "UPPER", "1name", "bad-name", "name;drop"} {
		if installIdentifierPattern.MatchString(value) {
			t.Errorf("不应接受 %q", value)
		}
	}
}
